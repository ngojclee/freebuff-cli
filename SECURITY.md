# Security

## No credentials in this repository

This repository ships no key, token, cookie, account identifier or private key. Every
credential is supplied by the operator at runtime and lives outside the repository:

| Credential | Where it lives |
| --- | --- |
| CodeBuddy account key (`ck_...`) | a CPA auth file, for example `codebuddy-you@example.com.json` |
| CodeBuddy sign-in session | a CPA auth file, written by the plugin after a management login, or imported from the CLI |
| `codebuddy --serve` gateway password | `~/.codebuddy/settings.json` on the sidecar host |
| CPA client keys | CPA's own `config.yaml` |

Test fixtures use obvious placeholders (`ck_abc`, `ck_imported`, `you@example.com`). The
repository history was scanned for the key shapes this project documents, and the scan found
no matches outside those fixtures.

## Handling

- The plugin never writes a credential to the log. Log lines carry the model name, the
  credential kind and an error class.
- Credentials are typed (`vendor_key`, `vendor_session`, `gateway`) so a vendor credential is
  never presented to the local server and a gateway password is never sent to the vendor.
- The plugin's only outbound calls are the vendor sign-in flow and the configured upstream
  endpoint. It does not read environment variables of the calling client.
- Rotating an account means replacing one auth file. Prefer that over `vendor_api_key` in the
  plugin config, which duplicates the secret into a file that is often copied and shared.

## Reporting

Open a private security advisory on the repository, or contact the maintainer directly, for
anything that looks like credential leakage, an auth bypass, or request smuggling through the
executor. Please do not open a public issue whose reproducer contains a live credential.
