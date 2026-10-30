# Corporate / custom CA certificates (optional)

If your network uses TLS inspection (e.g. a corporate proxy such as
Zscaler, Netskope, or an internal MITM appliance), outbound HTTPS calls
from inside the Docker container to the Dropbox API may fail with an error
like:

```text
tls: failed to verify certificate: x509: certificate signed by unknown authority
```

This happens because your host trusts the corporate root CA (installed in
the OS keychain/certificate store), but the minimal Docker runtime image
does not.

To fix it, place your organization's root CA certificate(s) in PEM format
in this directory, e.g.:

```text
certs/corporate-ca.pem
```

The Dockerfile automatically appends every `certs/*.pem` file to the
image's trusted CA bundle at build time. If this directory is empty (the
default), the build behaves exactly as before, using only the standard
public CA bundle.

**This directory's actual certificate files are gitignored** (only this
README is tracked) — root CA certificates are not typically secret, but
they are specific to your organization/network and should not be committed
to a public or shared repository.

## How to obtain your corporate root CA (macOS example)

```bash
# Find the certificate name as presented during a TLS handshake, e.g.:
echo | openssl s_client -connect api.dropboxapi.com:443 -servername api.dropboxapi.com -showcerts

# Export the matching intermediate/root certificate(s) from your Mac's
# System keychain (replace "YourCorpCA" with the CN shown above):
security find-certificate -a -c "YourCorpCA" -p /Library/Keychains/System.keychain > certs/corporate-ca.pem

# If the chain has multiple certs (e.g. an intermediate + a root), append
# each one to the same file so the full chain is trusted:
security find-certificate -a -c "YourCorpRootCA" -p /Library/Keychains/System.keychain >> certs/corporate-ca.pem
```

After adding the file(s), rebuild the image:

```bash
docker compose build --no-cache
docker compose run --rm dbsync sync --dry-run
```
