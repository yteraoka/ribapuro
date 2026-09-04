# ribapuro

Reverse Proxy して Response Body をファイルに保存する

リクエストの Host ヘッダーがそのまま upstream になる。`/etc/hosts` などで対象
ドメインをこのプロキシに向けた状態でも upstream を解決できるように、名前解決
にはシステムのリゾルバではなく `-resolver` で指定した DNS サーバーを使う。

保存先は `<-dir>/<host>/<path>` で、path が `/` で終わる場合は `index.html` を
付ける。Content-Encoding が gzip / deflate の場合は展開した内容を保存する
(クライアントへは元のまま返す)。

`-content-type` を指定すると、その MIME type にマッチする Response だけを保存
する (マッチしない Response もクライアントにはそのまま返す)。

## 使い方

```
ribapuro [flags]
```

| flag | 環境変数 | default | 説明 |
| --- | --- | --- | --- |
| `-addr` | `RIBAPURO_ADDR` | `:8080` | listen アドレス |
| `-dir` | `RIBAPURO_DIR` | `sites` | Response Body の保存先ディレクトリ |
| `-resolver` | `RIBAPURO_RESOLVER` | `1.1.1.1:53` | upstream の名前解決に使う DNS サーバー (空ならシステムのリゾルバ) |
| `-content-type` | `RIBAPURO_CONTENT_TYPE` | (なし) | 保存対象の MIME type パターン (指定なしなら全て保存) |
| `-shutdown-timeout` | | `10s` | graceful shutdown のタイムアウト |

### 保存対象の MIME type

`-content-type` は Response の `Content-Type` (`; charset=...` などのパラメータを
除いた `type/subtype` 部分) を glob パターンで比較する。大文字小文字は区別しない。

- 複数指定できる。flag を繰り返すか、`,` 区切りで並べる (環境変数の場合は `,` 区切り)
- `*` は `/` にマッチしないので、`text/*` は text 配下の全ての subtype にマッチする
- `/` を含まないパターンは type 全体の指定として扱う (`text` は `text/*`、`*` は `*/*`)
- `Content-Type` が無い Response は、パターンを指定している場合は保存されない

```
# HTML と CSS と JSON だけ保存する
ribapuro -content-type 'text/html,text/css' -content-type 'application/json'

# text 系と JSON 系 (application/problem+json なども) を保存する
ribapuro -content-type 'text/*,*/*json'

RIBAPURO_CONTENT_TYPE='text/*,image/*' ribapuro
```

## Docker

```
docker build -t ribapuro .
docker run --rm -p 8080:8080 -v "$PWD/sites:/app/sites" ribapuro
```

`sites` をホストの volume に mount する場合、コンテナ内のユーザー (uid 65532)
が書き込めるようにしておくこと。

## リリース

[tagpr](https://github.com/Songmu/tagpr) が main への push を監視し、未リリースの
変更があればバージョン更新用の Pull Request を自動作成する。その PR をマージす
ると tagpr が新しいタグを push し、[GoReleaser](https://goreleaser.com/) が
linux/darwin/windows (amd64/arm64) 向けバイナリをビルドして GitHub Release に
添付する。手元でリリース物を作る場合は次のコマンドで確認できる。

```
goreleaser release --snapshot --clean
```

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
