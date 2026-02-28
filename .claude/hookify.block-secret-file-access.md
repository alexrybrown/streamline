---
name: block-secret-file-access
enabled: true
event: file
action: block
conditions:
  - field: file_path
    operator: regex_match
    pattern: (\.env($|[^a-zA-Z])|\.pem$|\.key$|\.p12$|\.pfx$|\.crt$|\.jks$|\.keystore$|credentials\.json|service[-_]?account.*\.json|secrets?\.(json|ya?ml|toml)|token\.json|oauth.*\.json|\.aws/|\.boto$|\.azure/|\.gcloud/|\.docker/config\.json|docker-compose.*secrets|\.npmrc$|\.pypirc$|\.netrc$|id_(rsa|dsa|ecdsa|ed25519)|\.ssh/|\.gnupg/|kubeconfig|\.kube/config|\.tfvars($|[^a-zA-Z])|\.tfstate(\.backup)?$|\.pgpass$|(^|/)\.?my\.cnf$|\.htpasswd$|\.vault-token$|local\.properties$|wp-config\.php$|\._history$|\.bash_history$|\.zsh_history$|\.psql_history$)
---

**BLOCKED: access to a secret/sensitive file.** Do not read, write, or edit credential, key, env, or auth files. Use placeholder values if you need to describe their format.
