import React, { useContext } from "react";
import { find } from "lodash";
import { useQuery } from "react-query";
import { Tab, Tabs, TabList } from "react-tabs";
import { InjectedRouter } from "react-router";

import PATHS from "router/paths";
import { AppContext } from "context/app";
import mdmAppleAPI from "services/entities/mdm_apple";
import { IMdmApple } from "interfaces/mdm";

import TabsWrapper from "components/TabsWrapper";
import MainContent from "components/MainContent";
import TeamsDropdownHeader, {
  ITeamsDropdownState,
} from "components/PageHeader/TeamsDropdownHeader";
import EmptyTable from "components/EmptyTable";
import Button from "components/buttons/Button";
import Spinner from "components/Spinner";

interface IControlsSubNavItem {
  name: string;
  pathname: string;
}

const controlsSubNav: IControlsSubNavItem[] = [
  {
    name: "macOS updates",
    pathname: PATHS.CONTROLS_MAC_OS_UPDATES,
  },
  {
    name: "macOS settings",
    pathname: PATHS.CONTROLS_MAC_SETTINGS,
  },
];

interface IManageControlsPageProps {
  children: JSX.Element;
  location: any; // no type in react-router v3
  router: InjectedRouter; // v3
}

const getTabIndex = (path: string): number => {
  return controlsSubNav.findIndex((navItem) => {
    // tab stays highlighted for paths that start with same pathname
    return path.startsWith(navItem.pathname);
  });
};

const baseClass = "manage-controls-page";

const ManageControlsPage = ({
  children,
  location,
  router,
}: IManageControlsPageProps): JSX.Element => {
  const {
    availableTeams,
    isPremiumTier,
    currentTeam,
    setCurrentTeam,
  } = useContext(AppContext);

  const { data: mdmApple, isLoading: isLoadingMdmApple } = useQuery<
    IMdmApple,
    Error
  >(["mdmAppleAPI"], () => mdmAppleAPI.getAppleAPNInfo(), {
    enabled: isPremiumTier,
    staleTime: 5000,
    retry: false,
  });

  const navigateToNav = (i: number): void => {
    const navPath = controlsSubNav[i].pathname;
    const teamId = currentTeam?.id || undefined;
    const queryString = teamId === undefined ? "" : `?team_id=${teamId}`;
    router.replace(navPath + queryString);
  };

  const handleTeamSelect = (ctx: ITeamsDropdownState) => {
    const teamId = ctx.teamId;
    const queryString = teamId === undefined ? "" : `?team_id=${teamId}`;
    router.replace(location.pathname + queryString);
    const selectedTeam = find(availableTeams, ["id", teamId]);
    setCurrentTeam(selectedTeam);
  };

  const renderHeader = () => (
    <div className={`${baseClass}__header`}>
      <div className={`${baseClass}__text`}>
        <div className={`${baseClass}__title`}>
          <TeamsDropdownHeader
            router={router}
            location={location}
            baseClass={baseClass}
            defaultTitle="Controls"
            onChange={handleTeamSelect}
            description={() => {
              return null;
            }}
            includeNoTeams
            includeAll={false}
          />
        </div>
      </div>
    </div>
  );

  const onConnectClick = () => router.push(PATHS.ADMIN_INTEGRATIONS_MDM);

  const renderBody = () => {
    if (isLoadingMdmApple) {
      return <Spinner />;
    }
    return mdmApple ? (
      <div>
        <TabsWrapper>
          <Tabs
            selectedIndex={getTabIndex(location.pathname)}
            onSelect={(i) => navigateToNav(i)}
          >
            <TabList>
              {controlsSubNav.map((navItem) => {
                return (
                  <Tab key={navItem.name} data-text={navItem.name}>
                    {navItem.name}
                  </Tab>
                );
              })}
            </TabList>
          </Tabs>
        </TabsWrapper>
        {children}
      </div>
    ) : (
      <EmptyTable
        header="Manage your macOS hosts"
        info="Connect Fleet to the Apple Push Certificates Portal to get started."
        primaryButton={
          <Button
            variant="brand"
            onClick={onConnectClick}
            className={`${baseClass}__connectAPC-button`}
          >
            Connect
          </Button>
        }
      />
    );
  };

  return (
    <MainContent>
      <div className={`${baseClass}__wrapper`}>
        <div className={`${baseClass}__header-wrap`}>{renderHeader()}</div>
        {renderBody()}
      </div>
    </MainContent>
  );
};

export default ManageControlsPage;
