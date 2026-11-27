/** Proto <-> src/tracker and src/knowledge for what tracker and knowledge providers share: auth methods, option fields, hints. */
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { KnowledgeCapabilitiesSchema } from "../gen/harness/library/v1/knowledge_connections_pb.ts";
import { TrackerAuthMethod, type ProviderAuthHintSchema, type ProviderOptionFieldSchema, type TrackerCapabilitiesSchema } from "../gen/harness/library/v1/tracker_connections_pb.ts";
import type { KnowledgeCapabilities } from "../knowledge/provider.ts";
import { ApiError } from "../lib/errors.ts";
import type { AuthHint, AuthMethodName, Capabilities, OptionField } from "../tracker/provider.ts";

export const AUTH: Record<AuthMethodName, TrackerAuthMethod> = {
  token: TrackerAuthMethod.TOKEN,
  password: TrackerAuthMethod.PASSWORD,
  oauth: TrackerAuthMethod.OAUTH,
  email_token: TrackerAuthMethod.EMAIL_TOKEN,
  vcs: TrackerAuthMethod.VCS_CONNECTION,
};

export const authToProto = (m: string): TrackerAuthMethod => AUTH[m as AuthMethodName] ?? TrackerAuthMethod.UNSPECIFIED;

export function authName(m: TrackerAuthMethod): AuthMethodName {
  const found = (Object.entries(AUTH) as [AuthMethodName, TrackerAuthMethod][]).find(([, v]) => v === m);
  if (!found) throw new ApiError("invalid_request", "Choose how this connection signs in");
  return found[0];
}

/** "" for UNSPECIFIED: a knowledge connection that borrows has no method of its own. */
export const authNameOrEmpty = (m: TrackerAuthMethod): string => (m === TrackerAuthMethod.UNSPECIFIED ? "" : authName(m));

export function optionFieldsToProto(list: OptionField[] | undefined): MessageInitShape<typeof ProviderOptionFieldSchema>[] {
  return (list ?? []).map((f) => ({ key: f.key, label: f.label, placeholder: f.placeholder ?? "", required: !!f.required, allowedValues: f.allowedValues ?? [], help: f.help ?? "", defaultValue: f.defaultValue ?? "" }));
}

/** In the order of the methods, as the form lists them. */
export function authHintsToProto(methods: AuthMethodName[], hints: Partial<Record<AuthMethodName, AuthHint>> | undefined): MessageInitShape<typeof ProviderAuthHintSchema>[] {
  return methods.flatMap((m) => {
    const h = hints?.[m];
    return h ? [{ method: AUTH[m], help: h.help, secretLabel: h.secretLabel ?? "", loginLabel: h.loginLabel ?? "" }] : [];
  });
}

export function capabilitiesToProto(c: Capabilities): MessageInitShape<typeof TrackerCapabilitiesSchema> {
  return {
    provider: c.provider,
    displayName: c.displayName,
    authMethods: c.authMethods.map((m) => AUTH[m]),
    queryLanguage: c.queryLanguage,
    queryExample: c.queryExample,
    customFields: c.customFields,
    comments: c.comments,
    optionFields: optionFieldsToProto(c.optionFields),
    docsUrl: c.docsUrl ?? "",
    logo: c.logo ?? "",
    baseUrlPlaceholder: c.baseUrlPlaceholder ?? "",
    defaultBaseUrl: c.defaultBaseUrl ?? "",
    authHints: authHintsToProto(c.authMethods, c.authHints),
  };
}

export function knowledgeCapabilitiesToProto(c: KnowledgeCapabilities): MessageInitShape<typeof KnowledgeCapabilitiesSchema> {
  return {
    provider: c.provider,
    displayName: c.displayName,
    description: c.description,
    authMethods: c.authMethods.map((m) => AUTH[m]),
    optionFields: optionFieldsToProto(c.optionFields),
    docsUrl: c.docsUrl ?? "",
    logo: c.logo ?? "",
    baseUrlPlaceholder: c.baseUrlPlaceholder ?? "",
    defaultBaseUrl: c.defaultBaseUrl ?? "",
    authHints: authHintsToProto(c.authMethods, c.authHints),
    borrowFrom: c.borrowFrom,
    queryLanguage: c.queryLanguage ?? "",
    queryExample: c.queryExample ?? "",
    tree: !!c.tree,
  };
}
