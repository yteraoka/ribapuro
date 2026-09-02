# ribapuro

Reverse Proxy して Response Body をファイルに保存する

リクエストの Host ヘッダーがそのまま upstream になる。`/etc/hosts` などで対象
ドメインをこのプロキシに向けた状態でも upstream を解決できるように、名前解決
にはシステムのリゾルバではなく `-resolver` で指定した DNS サーバーを使う。

保存先は `<-dir>/<host>/<path>` で、path が `/` で終わる場合は `index.html` を
付ける。Content-Encoding が gzip / deflate の場合は展開した内容を保存する
(クライアントへは元のまま返す)。

## 使い方

```
ribapuro [flags]
```

| flag | 環境変数 | default | 説明 |
| --- | --- | --- | --- |
| `-addr` | `RIBAPURO_ADDR` | `:8080` | listen アドレス |
| `-dir` | `RIBAPURO_DIR` | `sites` | Response Body の保存先ディレクトリ |
| `-resolver` | `RIBAPURO_RESOLVER` | `1.1.1.1:53` | upstream の名前解決に使う DNS サーバー (空ならシステムのリゾルバ) |
| `-shutdown-timeout` | | `10s` | graceful shutdown のタイムアウト |

## Docker

```
docker build -t ribapuro .
docker run --rm -p 8080:8080 -v "$PWD/sites:/app/sites" ribapuro
```

`sites` をホストの volume に mount する場合、コンテナ内のユーザー (uid 65532)
が書き込めるようにしておくこと。

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
