### Fixed

- Claude OAuth login now sends a loopback `redirect_uri` (`http://localhost:<port>/callback`) instead of the dashboard origin, so it no longer fails with "Redirect URI ... is not supported by client" when the dashboard runs on a custom domain (#1266).
