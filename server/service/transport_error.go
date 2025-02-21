package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/fleetdm/fleet/v4/server/contexts/ctxerr"
	"github.com/fleetdm/fleet/v4/server/contexts/host"
	"github.com/fleetdm/fleet/v4/server/contexts/viewer"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/getsentry/sentry-go"
	kithttp "github.com/go-kit/kit/transport/http"
	"github.com/go-sql-driver/mysql"
)

// errorer interface is implemented by response structs to encode business logic errors
type errorer interface {
	error() error
}

type jsonError struct {
	Message string              `json:"message"`
	Code    int                 `json:"code,omitempty"`
	Errors  []map[string]string `json:"errors,omitempty"`
}

// use baseError to encode an jsonError.Errors field with an error that has
// a generic "name" field. The frontend client always expects errors in a
// []map[string]string format.
func baseError(err string) []map[string]string {
	return []map[string]string{
		{
			"name":   "base",
			"reason": err,
		},
	}
}

type validationErrorInterface interface {
	error
	Invalid() []map[string]string
}

type permissionErrorInterface interface {
	error
	PermissionError() []map[string]string
}

type badRequestErrorInterface interface {
	error
	BadRequestError() []map[string]string
}

type notFoundErrorInterface interface {
	error
	IsNotFound() bool
}

type existsErrorInterface interface {
	error
	IsExists() bool
}

type conflictErrorInterface interface {
	error
	IsConflict() bool
}

func encodeErrorAndTrySentry(sentryEnabled bool) func(ctx context.Context, err error, w http.ResponseWriter) {
	if !sentryEnabled {
		return encodeError
	}
	return func(ctx context.Context, err error, w http.ResponseWriter) {
		encodeError(ctx, err, w)
		sendToSentry(ctx, err)
	}
}

// encode error and status header to the client
func encodeError(ctx context.Context, err error, w http.ResponseWriter) {
	ctxerr.Handle(ctx, err)
	origErr := err

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	err = ctxerr.Cause(err)

	switch e := err.(type) {
	case validationErrorInterface:
		ve := jsonError{
			Message: "Validation Failed",
			Errors:  e.Invalid(),
		}
		if statusErr, ok := e.(statuser); ok {
			w.WriteHeader(statusErr.Status())
		} else {
			w.WriteHeader(http.StatusUnprocessableEntity)
		}
		enc.Encode(ve) //nolint:errcheck
	case permissionErrorInterface:
		pe := jsonError{
			Message: "Permission Denied",
			Errors:  e.PermissionError(),
		}
		w.WriteHeader(http.StatusForbidden)
		enc.Encode(pe) //nolint:errcheck
		return
	case mailError:
		me := jsonError{
			Message: "Mail Error",
			Errors:  e.MailError(),
		}
		w.WriteHeader(http.StatusInternalServerError)
		enc.Encode(me) //nolint:errcheck
	case osqueryError:
		// osquery expects to receive the node_invalid key when a TLS
		// request provides an invalid node_key for authentication. It
		// doesn't use the error message provided, but we provide this
		// for debugging purposes (and perhaps osquery will use this
		// error message in the future).

		errMap := map[string]interface{}{"error": e.Error()}
		if e.NodeInvalid() {
			w.WriteHeader(http.StatusUnauthorized)
			errMap["node_invalid"] = true
		} else {
			// TODO: osqueryError is not always the result of an internal error on
			// our side, it is also used to represent a client error (invalid data,
			// e.g. malformed json, carve too large, etc., so 4xx), are we returning
			// a 500 because of some osquery-specific requirement?
			w.WriteHeader(http.StatusInternalServerError)
		}

		enc.Encode(errMap) //nolint:errcheck
	case notFoundErrorInterface:
		je := jsonError{
			Message: "Resource Not Found",
			Errors:  baseError(e.Error()),
		}
		w.WriteHeader(http.StatusNotFound)
		enc.Encode(je) //nolint:errcheck
	case existsErrorInterface:
		je := jsonError{
			Message: "Resource Already Exists",
			Errors:  baseError(e.Error()),
		}
		w.WriteHeader(http.StatusConflict)
		enc.Encode(je) //nolint:errcheck
	case conflictErrorInterface:
		je := jsonError{
			Message: "Conflict",
			Errors:  baseError(e.Error()),
		}
		w.WriteHeader(http.StatusConflict)
		enc.Encode(je) //nolint:errcheck
	case badRequestErrorInterface:
		je := jsonError{
			Message: "Bad request",
			Errors:  baseError(e.Error()),
		}
		w.WriteHeader(http.StatusBadRequest)
		enc.Encode(je) //nolint:errcheck
	case *mysql.MySQLError:
		je := jsonError{
			Message: "Validation Failed",
			Errors:  baseError(e.Error()),
		}
		statusCode := http.StatusUnprocessableEntity
		if e.Number == 1062 {
			statusCode = http.StatusConflict
		}
		w.WriteHeader(statusCode)
		enc.Encode(je) //nolint:errcheck
	case *fleet.Error:
		je := jsonError{
			Message: e.Error(),
			Code:    e.Code,
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		enc.Encode(je) //nolint:errcheck
	default:
		// when there's a tcp read timeout, the error is *net.OpError but the cause is an internal
		// poll.DeadlineExceeded which we cannot match against, so we match against the original error
		var opErr *net.OpError
		if errors.As(origErr, &opErr) {
			w.WriteHeader(http.StatusRequestTimeout)
			je := jsonError{
				Message: opErr.Error(),
				Errors:  baseError(opErr.Error()),
			}
			enc.Encode(je) //nolint:errcheck
			return
		}
		if fleet.IsForeignKey(err) {
			ve := jsonError{
				Message: "Validation Failed",
				Errors:  baseError(err.Error()),
			}
			w.WriteHeader(http.StatusUnprocessableEntity)
			enc.Encode(ve) //nolint:errcheck
			return
		}

		// Get specific status code if it is available from this error type,
		// defaulting to HTTP 500
		status := http.StatusInternalServerError
		var sce kithttp.StatusCoder
		if errors.As(err, &sce) {
			status = sce.StatusCode()
		}

		// See header documentation
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Retry-After)
		var ewra fleet.ErrWithRetryAfter
		if errors.As(err, &ewra) {
			w.Header().Add("Retry-After", strconv.Itoa(ewra.RetryAfter()))
		}

		msg := err.Error()
		reason := err.Error()
		var ume *fleet.UserMessageError
		if errors.As(err, &ume) {
			if text := http.StatusText(status); text != "" {
				msg = text
			}
			reason = ume.UserMessage()
		}

		w.WriteHeader(status)
		je := jsonError{
			Message: msg,
			Errors:  baseError(reason),
		}
		enc.Encode(je) //nolint:errcheck
	}
}

func sendToSentry(ctx context.Context, err error) {
	v, haveUser := viewer.FromContext(ctx)
	h, haveHost := host.FromContext(ctx)
	localHub := sentry.CurrentHub().Clone()
	if haveUser {
		localHub.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetTag("email", v.User.Email)
			scope.SetTag("user_id", fmt.Sprint(v.User.ID))
		})
	} else if haveHost {
		localHub.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetTag("hostname", h.Hostname)
			scope.SetTag("host_id", fmt.Sprint(h.ID))
		})
	}
	localHub.CaptureException(err)
}
