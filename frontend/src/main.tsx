import "./styles.css";

import { lazy, StrictMode, Suspense } from "react";
import { createRoot } from "react-dom/client";
import {
  createBrowserRouter,
  Navigate,
  RouterProvider,
} from "react-router-dom";

import { AppShell } from "./app-shell";
import { basePath } from "./base";
import { installPreloadRecovery } from "./preload-recovery";
import { RouteReady } from "./route-ready";

installPreloadRecovery(window);

const AccountPage = lazy(() =>
  import("./pages/account").then((module) => ({ default: module.AccountPage })),
);
const ApiDocsPage = lazy(() =>
  import("./pages/api-docs").then((module) => ({
    default: module.ApiDocsPage,
  })),
);
const AuthPage = lazy(() =>
  import("./pages/auth").then((module) => ({ default: module.AuthPage })),
);
const InsightsPage = lazy(() =>
  import("./pages/insights").then((module) => ({
    default: module.InsightsPage,
  })),
);
const OverviewPage = lazy(() =>
  import("./pages/overview").then((module) => ({
    default: module.OverviewPage,
  })),
);
const OAuthConsentPage = lazy(() =>
  import("./pages/oauth-consent").then((module) => ({
    default: module.OAuthConsentPage,
  })),
);
const ProjectPage = lazy(() =>
  import("./pages/projects").then((module) => ({
    default: module.ProjectPage,
  })),
);
const ProjectsPage = lazy(() =>
  import("./pages/projects").then((module) => ({
    default: module.ProjectsPage,
  })),
);
const PublicOverviewPage = lazy(() =>
  import("./pages/public").then((module) => ({
    default: module.PublicOverviewPage,
  })),
);
const PublicProjectPage = lazy(() =>
  import("./pages/public").then((module) => ({
    default: module.PublicProjectPage,
  })),
);
const PublicSessionPage = lazy(() =>
  import("./pages/public").then((module) => ({
    default: module.PublicSessionPage,
  })),
);
const SessionPage = lazy(() =>
  import("./pages/session-detail").then((module) => ({
    default: module.SessionPage,
  })),
);
const SessionsPage = lazy(() =>
  import("./pages/sessions").then((module) => ({
    default: module.SessionsPage,
  })),
);

const router = createBrowserRouter(
  [
    {
      path: "/login",
      element: (
        <RouteReady title="Log in">
          <AuthPage mode="login" />
        </RouteReady>
      ),
    },
    {
      path: "/register",
      element: (
        <RouteReady title="Register">
          <AuthPage mode="register" />
        </RouteReady>
      ),
    },
    {
      path: "/api/docs",
      element: (
        <RouteReady title="API">
          <ApiDocsPage />
        </RouteReady>
      ),
    },
    {
      path: "/oauth/authorize",
      element: (
        <RouteReady title="Authorize">
          <OAuthConsentPage />
        </RouteReady>
      ),
    },
    {
      path: "/u/:username",
      element: (
        <RouteReady>
          <PublicOverviewPage />
        </RouteReady>
      ),
    },
    {
      path: "/p/:id",
      element: (
        <RouteReady>
          <PublicProjectPage />
        </RouteReady>
      ),
    },
    {
      path: "/s/:publicId",
      element: (
        <RouteReady>
          <PublicSessionPage />
        </RouteReady>
      ),
    },
    {
      element: <AppShell />,
      children: [
        {
          path: "/overview",
          element: (
            <RouteReady title="Overview">
              <OverviewPage />
            </RouteReady>
          ),
        },
        {
          path: "/insights",
          element: (
            <RouteReady title="Insights">
              <InsightsPage />
            </RouteReady>
          ),
        },
        {
          path: "/projects",
          element: (
            <RouteReady title="Projects">
              <ProjectsPage />
            </RouteReady>
          ),
        },
        {
          path: "/projects/:id",
          element: (
            <RouteReady title="Project">
              <ProjectPage />
            </RouteReady>
          ),
        },
        {
          path: "/sessions",
          element: (
            <RouteReady title="Sessions">
              <SessionsPage />
            </RouteReady>
          ),
        },
        {
          path: "/sessions/:id",
          element: (
            <RouteReady title="Session">
              <SessionPage />
            </RouteReady>
          ),
        },
        {
          path: "/account",
          element: (
            <RouteReady title="Account">
              <AccountPage />
            </RouteReady>
          ),
        },
      ],
    },
    { path: "*", element: <Navigate to="/overview" replace /> },
    // The basename mirrors the external path prefix the server injected, so the
    // history URLs the router writes match what the reverse proxy serves.
  ],
  { basename: basePath || "/" },
);

const root = document.getElementById("root");
if (!root) throw new Error("missing React root");
createRoot(root).render(
  <StrictMode>
    <Suspense
      fallback={
        <div className="route-loading" role="status">
          Loading view...
        </div>
      }
    >
      <RouterProvider router={router} />
    </Suspense>
  </StrictMode>,
);
