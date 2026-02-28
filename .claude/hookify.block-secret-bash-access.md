---
name: block-secret-bash-access
enabled: true
event: bash
action: block
pattern: (cat|head|tail|less|more|bat|vi|vim|nano|code|open|pbcopy|xclip|base64|xxd|strings|file|stat|wc|diff|rg|grep|awk|sed|sort|cut|tee|source|cp|mv|scp|rsync|\.)\s+.*(\.(env|pem|key|p12|pfx|crt|jks|keystore|tfvars|tfstate|pgpass|htpasswd|vault-token|boto)([^a-zA-Z]|$)|credentials\.json|service[-_]?account.*\.json|secrets?\.(json|ya?ml|toml)|token\.json|oauth.*\.json|\.aws/|\.azure/|\.gcloud/|\.docker/config\.json|\.npmrc|\.pypirc|\.netrc|id_(rsa|dsa|ecdsa|ed25519)|\.ssh/|\.gnupg/|kubeconfig|\.kube/config|my\.cnf|local\.properties|wp-config\.php|\.bash_history|\.zsh_history|\.psql_history|\._history)
---

**BLOCKED: command accesses a secret/sensitive file.** Do not read, copy, or process credential, key, env, or auth files. Use placeholder values if you need to describe their format.
