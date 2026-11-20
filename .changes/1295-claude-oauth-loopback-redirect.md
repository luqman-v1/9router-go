### Fixed

- Claude OAuth login on a custom domain: the PKCE authorize step now sends a
  loopback `redirect_uri` (`http://localhost:<port>/callback`) instead of the
  dashboard origin. Claude's public OAuth client only accepts loopback
  callbacks, so the dashboard origin made `claude.ai` reject the authorize step
  with "Redirect URI … is not supported by client" (#1266).