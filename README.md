# Stargate Proxy
A forward proxy with support for TLS Tunneling and caching with redis.

## How to use
1. Create a self-signed certificate.
```bash
openssl req -new -newkey rsa:2048 -days 3650 -nodes -x509 -keyout myCA.key -out myCA.pem
```
2. Optional: Load the certificate to your local system.
```bash
sudo cp myCA.pem /etc/pki/ca-trust/source/anchors/
sudo update-ca-trust
```

3. Build the docker image.
```bash
docker build -t stargate-proxy .
```
4. Create a docker compose file with the following content and change it to your needs.
```yaml
version: '3.7'
services:
  stargate-proxy:
    image: "stargate-proxy"
    ports:
      - 8080:8080
    volumes:
      - /path/to/myCA.pem:/etc/stargate-proxy/myCA.pem
      - /path/to/myCA.key:/etc/stargate-proxy/myCA.key
    environment:
      SP_HOSTNAME: "0.0.0.0"
      SP_PORT: "8080"
      SP_CERT_FILE: "/etc/stargate-proxy/myCA.pem"
      SP_KEY_FILE: "/etc/stargate-proxy/myCA.key"
      SP_REDIS_HOSTNAME: "rediscache"
      SP_REDIS_PORT: "6379"
  redis:
    image: "redis:alpine"
    hostname: "rediscache"
    ports:
      - 6379:6379
```
