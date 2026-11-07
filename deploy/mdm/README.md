# Device management templates

Templates for putting xlsft\`s harness policy on managed machines: a macOS configuration profile, the preference file Intune
takes, a Jamf Pro custom schema, a Windows administrative template (ADMX and ADML) and a `.reg` example. [keys.md](keys.md)
lists every key. [docs/enterprise.md](../../docs/enterprise.md) says how to deploy them, with the installer package and an
offline update mirror.

Everything here but this file is generated from `packages/client/settings/policy.schema.json`: change the schema, then run
`scripts/policy-templates.sh`. The client's tests fail while these files and the schema disagree.
