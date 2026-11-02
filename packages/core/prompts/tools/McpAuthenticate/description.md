Starts the authorization of the MCP server "{{server}}", which needs the user to sign in before its tools can be used. Returns a link for the user to open in a browser.

- Call it only when the user's request needs this server.
- Give the link to the user and ask them to complete the sign-in; do not try to open it yourself.
- The authorization is stored on the harness server and applies to every device. The server's tools become available in a later session.
