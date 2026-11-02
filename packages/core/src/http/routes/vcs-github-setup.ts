/**
 * GET /api/vcs/github/setup?installation_id=...&setup_action=install|update — where GitHub sends the browser
 * after the app was installed. The installation itself is recorded from the installation webhook.
 */
import { defineHandler } from "h3";
import { resultPage } from "../result-page.ts";

export default defineHandler((event) => {
  const action = event.url.searchParams.get("setup_action") === "update" ? "updated" : "installed";
  return resultPage(true, `GitHub App ${action}`, "harness now receives pull request and CI events for the selected repositories. You can close this window.", 200);
});
