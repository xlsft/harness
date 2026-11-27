/**
 * The "vcs" sign-in of a company connection: through a code host connection from Settings > VCS, named by the option
 * "vcs_connection_id". GitLab gives that connection's access token; GitHub an installation token of its App, for the account in
 * the option "vcs_owner" or the first installation. Tokens are fetched when a request needs one and never stored here.
 */
import { ProviderType } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { connSecrets, getConnectionRow } from "../vcs/connections.ts";
import { installationToken, listInstallations } from "../vcs/github-app.ts";
import type { VcsSignIn } from "./provider.ts";

/** The option keys a provider with the "vcs" method declares among its optionFields. */
export const VCS_OPTION_FIELDS = [
  { key: "vcs_connection_id", label: "Code host connection", required: false, help: "The id of a connection from Settings > VCS. On a device, gh or glab signs in instead." },
  { key: "vcs_owner", label: "GitHub account or organization", required: false, help: "Which installation of the GitHub App to use; the first one when empty." },
];

export function vcsSignIn(options: Record<string, string>): () => Promise<VcsSignIn> {
  return async () => {
    const id = options.vcs_connection_id ?? "";
    if (!id) throw new ApiError("invalid_request", "Choose the code host connection this signs in through");
    const row = await getConnectionRow(id);
    if (row.type === ProviderType.GITLAB) {
      return {
        type: "gitlab",
        baseUrl: row.baseUrl,
        token: async () => {
          const t = connSecrets(row).accessToken;
          if (!t) throw new ApiError("failed_precondition", "The GitLab connection has no access token");
          return t;
        },
      };
    }
    return {
      type: "github",
      baseUrl: row.baseUrl,
      token: async () => {
        const owner = (options.vcs_owner ?? "").toLowerCase();
        const list = await listInstallations(row);
        const inst = (owner && list.find((i) => i.login.toLowerCase() === owner)) || list[0];
        if (!inst) throw new ApiError("failed_precondition", "The GitHub App has no installation to sign in with");
        return installationToken(row, inst.id);
      },
    };
  };
}
