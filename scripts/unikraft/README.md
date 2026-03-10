# Unikraft test scripts

To create an instance and connect

export METRO=<your-metro>
export KRAFTCLOUD_USER=<user>
export KRAFTCLOUD_TOKEN=$(op read "op://Employee/unikraft-cci-org/credential")


```
./deploy-unikraft.sh <your args> # e.g. deploy-unikraft.sh --env "SSH_PUBLIC_KEY=${SSH_PUBLIC_KEY}" \
  --port 2222:2222/tls \
  -M 2Gi
kraft cloud instance start <instance-name-from-first-command>
./connect-unikraft.sh <fqdn of instance>
```