package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fleetdm/fleet/v4/server"
	"github.com/fleetdm/fleet/v4/server/contexts/ctxerr"
	hostctx "github.com/fleetdm/fleet/v4/server/contexts/host"
	"github.com/fleetdm/fleet/v4/server/contexts/logging"
	"github.com/fleetdm/fleet/v4/server/fleet"
)

type setOrbitNodeKeyer interface {
	setOrbitNodeKey(nodeKey string)
}

type orbitError struct {
	message string
}

type EnrollOrbitRequest struct {
	EnrollSecret   string `json:"enroll_secret"`
	HardwareUUID   string `json:"hardware_uuid"`
	HardwareSerial string `json:"hardware_serial"`
}

type EnrollOrbitResponse struct {
	OrbitNodeKey string `json:"orbit_node_key,omitempty"`
	Err          error  `json:"error,omitempty"`
}

type orbitGetConfigRequest struct {
	OrbitNodeKey string `json:"orbit_node_key"`
}

func (r *orbitGetConfigRequest) setOrbitNodeKey(nodeKey string) {
	r.OrbitNodeKey = nodeKey
}

func (r *orbitGetConfigRequest) orbitHostNodeKey() string {
	return r.OrbitNodeKey
}

type orbitGetConfigResponse struct {
	fleet.OrbitConfig
	Err error `json:"error,omitempty"`
}

func (r orbitGetConfigResponse) error() error { return r.Err }

func (e orbitError) Error() string {
	return e.message
}

func (r EnrollOrbitResponse) error() error { return r.Err }

// hijackRender so we can add a header with the server capabilities in the
// response, allowing Orbit to know what features are available without the
// need to enroll.
func (r EnrollOrbitResponse) hijackRender(ctx context.Context, w http.ResponseWriter) {
	writeCapabilitiesHeader(w, fleet.ServerOrbitCapabilities)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(r); err != nil {
		encodeError(ctx, osqueryError{message: fmt.Sprintf("orbit enroll failed: %s", err)}, w)
	}
}

func enrollOrbitEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (errorer, error) {
	req := request.(*EnrollOrbitRequest)
	nodeKey, err := svc.EnrollOrbit(ctx, req.HardwareUUID, req.HardwareSerial, req.EnrollSecret)
	if err != nil {
		return EnrollOrbitResponse{Err: err}, nil
	}
	return EnrollOrbitResponse{OrbitNodeKey: nodeKey}, nil
}

func (svc *Service) AuthenticateOrbitHost(ctx context.Context, orbitNodeKey string) (*fleet.Host, bool, error) {
	svc.authz.SkipAuthorization(ctx)

	if orbitNodeKey == "" {
		return nil, false, ctxerr.Wrap(ctx, fleet.NewAuthRequiredError("authentication error: missing orbit node key"))
	}

	host, err := svc.ds.LoadHostByOrbitNodeKey(ctx, orbitNodeKey)
	switch {
	case err == nil:
		// OK
	case fleet.IsNotFound(err):
		return nil, false, ctxerr.Wrap(ctx, fleet.NewAuthRequiredError("authentication error: invalid orbit node key"))
	default:
		return nil, false, ctxerr.Wrap(ctx, err, "authentication error orbit")
	}

	return host, svc.debugEnabledForHost(ctx, host.ID), nil
}

// EnrollOrbit returns an orbit nodeKey on successful enroll
func (svc *Service) EnrollOrbit(ctx context.Context, hardwareUUID, hardwareSerial, enrollSecret string) (string, error) {
	// this is not a user-authenticated endpoint
	svc.authz.SkipAuthorization(ctx)
	logging.WithExtras(ctx, "hardware_uuid", hardwareUUID, "hardware_serial", hardwareSerial)

	secret, err := svc.ds.VerifyEnrollSecret(ctx, enrollSecret)
	if err != nil {
		return "", orbitError{message: err.Error()}
	}

	orbitNodeKey, err := server.GenerateRandomText(svc.config.Osquery.NodeKeySize)
	if err != nil {
		return "", orbitError{message: "failed to generate orbit node key: " + err.Error()}
	}

	appConfig, err := svc.ds.AppConfig(ctx)
	if err != nil {
		return "", orbitError{message: "app config load failed: " + err.Error()}
	}

	_, err = svc.ds.EnrollOrbit(ctx, appConfig.MDM.EnabledAndConfigured, hardwareUUID, hardwareSerial, orbitNodeKey, secret.TeamID)
	if err != nil {
		return "", orbitError{message: "failed to enroll " + err.Error()}
	}

	return orbitNodeKey, nil
}

func getOrbitConfigEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (errorer, error) {
	cfg, err := svc.GetOrbitConfig(ctx)
	if err != nil {
		return orbitGetConfigResponse{Err: err}, nil
	}
	return orbitGetConfigResponse{OrbitConfig: cfg}, nil
}

func (svc *Service) GetOrbitConfig(ctx context.Context) (fleet.OrbitConfig, error) {
	// this is not a user-authenticated endpoint
	svc.authz.SkipAuthorization(ctx)

	var notifs fleet.OrbitConfigNotifications

	host, ok := hostctx.FromContext(ctx)
	if !ok {
		return fleet.OrbitConfig{Notifications: notifs}, orbitError{message: "internal error: missing host from request context"}
	}

	// set the host's orbit notifications
	if host.IsOsqueryEnrolled() && host.MDMInfo.IsPendingDEPFleetEnrollment() {
		notifs.RenewEnrollmentProfile = true
	}

	// team ID is not nil, get team specific flags and options
	if host.TeamID != nil {
		teamAgentOptions, err := svc.ds.TeamAgentOptions(ctx, *host.TeamID)
		if err != nil {
			return fleet.OrbitConfig{Notifications: notifs}, err
		}

		var opts fleet.AgentOptions
		if teamAgentOptions != nil && len(*teamAgentOptions) > 0 {
			if err := json.Unmarshal(*teamAgentOptions, &opts); err != nil {
				return fleet.OrbitConfig{Notifications: notifs}, err
			}
		}

		mdmConfig, err := svc.ds.TeamMDMConfig(ctx, *host.TeamID)
		if err != nil {
			return fleet.OrbitConfig{Notifications: notifs}, err
		}

		var nudgeConfig *fleet.NudgeConfig
		if mdmConfig != nil &&
			mdmConfig.MacOSUpdates.Deadline != "" &&
			mdmConfig.MacOSUpdates.MinimumVersion != "" {
			nudgeConfig, err = fleet.NewNudgeConfig(mdmConfig.MacOSUpdates)
			if err != nil {
				return fleet.OrbitConfig{Notifications: notifs}, err
			}
		}

		return fleet.OrbitConfig{
			Flags:         opts.CommandLineStartUpFlags,
			Extensions:    opts.Extensions,
			Notifications: notifs,
			NudgeConfig:   nudgeConfig,
		}, nil
	}

	// team ID is nil, get global flags and options
	config, err := svc.ds.AppConfig(ctx)
	if err != nil {
		return fleet.OrbitConfig{Notifications: notifs}, err
	}
	var opts fleet.AgentOptions
	if config.AgentOptions != nil {
		if err := json.Unmarshal(*config.AgentOptions, &opts); err != nil {
			return fleet.OrbitConfig{Notifications: notifs}, err
		}
	}

	var nudgeConfig *fleet.NudgeConfig
	if config.MDM.MacOSUpdates.Deadline != "" &&
		config.MDM.MacOSUpdates.MinimumVersion != "" {
		nudgeConfig, err = fleet.NewNudgeConfig(config.MDM.MacOSUpdates)
		if err != nil {
			return fleet.OrbitConfig{Notifications: notifs}, err
		}
	}

	return fleet.OrbitConfig{
		Flags:         opts.CommandLineStartUpFlags,
		Extensions:    opts.Extensions,
		Notifications: notifs,
		NudgeConfig:   nudgeConfig,
	}, nil
}

/////////////////////////////////////////////////////////////////////////////////
// Ping orbit endpoint
/////////////////////////////////////////////////////////////////////////////////

type orbitPingRequest struct{}

type orbitPingResponse struct{}

func (r orbitPingResponse) hijackRender(ctx context.Context, w http.ResponseWriter) {
	writeCapabilitiesHeader(w, fleet.ServerOrbitCapabilities)
}

func (r orbitPingResponse) error() error { return nil }

// NOTE: we're intentionally not reading the capabilities header in this
// endpoint as is unauthenticated and we don't want to trust whatever comes in
// there.
func orbitPingEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (errorer, error) {
	svc.DisableAuthForPing(ctx)
	return orbitPingResponse{}, nil
}

/////////////////////////////////////////////////////////////////////////////////
// SetOrUpdateDeviceToken endpoint
/////////////////////////////////////////////////////////////////////////////////

type setOrUpdateDeviceTokenRequest struct {
	OrbitNodeKey    string `json:"orbit_node_key"`
	DeviceAuthToken string `json:"device_auth_token"`
}

func (r *setOrUpdateDeviceTokenRequest) setOrbitNodeKey(nodeKey string) {
	r.OrbitNodeKey = nodeKey
}

func (r *setOrUpdateDeviceTokenRequest) orbitHostNodeKey() string {
	return r.OrbitNodeKey
}

type setOrUpdateDeviceTokenResponse struct {
	Err error `json:"error,omitempty"`
}

func (r setOrUpdateDeviceTokenResponse) error() error { return r.Err }

func setOrUpdateDeviceTokenEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (errorer, error) {
	req := request.(*setOrUpdateDeviceTokenRequest)
	if err := svc.SetOrUpdateDeviceAuthToken(ctx, req.DeviceAuthToken); err != nil {
		return setOrUpdateDeviceTokenResponse{Err: err}, nil
	}
	return setOrUpdateDeviceTokenResponse{}, nil
}

func (svc *Service) SetOrUpdateDeviceAuthToken(ctx context.Context, deviceAuthToken string) error {
	// this is not a user-authenticated endpoint
	svc.authz.SkipAuthorization(ctx)

	host, ok := hostctx.FromContext(ctx)
	if !ok {
		return osqueryError{message: "internal error: missing host from request context"}
	}

	if err := svc.ds.SetOrUpdateDeviceAuthToken(ctx, host.ID, deviceAuthToken); err != nil {
		return osqueryError{
			message: fmt.Sprintf("internal error: failed to set or update device auth token: %e", err),
		}
	}

	return nil
}
