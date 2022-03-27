# ribapuro

Reverse Proxy して Response Body をファイルに保存する

# Caddy で TLS 終端

次のような内容の Caddyfile を用意して

```
example.com

tls internal
reverse_proxy localhost:8080
```

caddy run で proxy する

```
caddy run --config Caddyfile
```

https://blog.1q77.com/2020/08/one-liner-https-reverse-proxy-caddy/
