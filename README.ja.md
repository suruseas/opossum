<p align="center">
  <img src="docs/assets/readme-banner.png" alt="opossum — Apple container ランタイム向けの Compose 風オーケストレータ" width="920">
</p>

<p align="right"><a href="README.md">English</a></p>

# opossum — Apple container ランタイムで docker compose のプロジェクトを動かす

Apple の `container` はコンテナを 1 個ずつ起動します——compose ファイルも、依存関係の順序も、名前によるサービス発見もありません。opossum は、いま手元にある `compose.yaml` をそのまま `container` の上で動かします。Docker Desktop もデーモンも要りません。

Docker Compose ファイル（`docker-compose.yml`）を読み、オープンな [Compose 仕様](https://compose-spec.io) のサブセットを実装しています。サービスはプロジェクトごとの共有ネットワーク上で依存関係の順に起動し、互いを名前で解決できます。macOS 26 以降と [Apple `container`](https://github.com/apple/container) が前提です。

<!-- compat-figures -->
実運用の compose プロジェクト 156 本での実測：無改変で完走 61 本（39%）、`--from-docker-compose` を付けて 78 本（50%）、悪化 0 本。方法と、まだ何が邪魔をしているかの内訳は [measured compatibility](docs/compatibility.md)（英語）にあります。
<!-- /compat-figures -->

> **これは英語版 [README.md](README.md) の日本語訳です。** 内容の正本は英語版で、細部で差が出た場合は英語版が優先されます。

> **AI エージェントから opossum を使う場合は** [`AGENTS.md`](AGENTS.md) を読ませてください。コマンド一覧、対応・無視・拒否される compose フィールド、失敗シグネチャと対処の対応表を、エージェントのコンテキストに読み込ませる前提で事実だけを高密度にまとめてあります。人間が読む分には、以下のクイックスタートで大丈夫です。

> **なぜ今これが可能になったのか：** コンテナ間のネットワークと名前解決は **macOS 26** の機能に依存しています。macOS 15 まではコンテナ同士がネットワーク的に隔離されていて、この種のオーケストレーションは成立しませんでした。`container` は 2026 年 6 月に 1.0 に到達しています。

## なぜ opossum か（他の選択肢ではなく）

3つ、それぞれ実測の裏付けとともに。

**1. 実運用の compose ファイルで測ってある。**
[Compose-Examples](https://github.com/Haxxnet/Compose-Examples) の自己ホスト用
プロジェクト 156 本を無改変で実行：無改変のまま完走が 61 本（39%）、
`--from-docker-compose` を付けて 78 本（50%）、悪化 0 本。残った失敗はすべて、
生のランタイムエラーではなく診断コードと対処の提案として報告されます。
[方法・母数・内訳（英語）→](docs/compatibility.md)

**2. 非互換をこちらで直す。** `opossum up --from-docker-compose` は、Apple
`container` に必要な調整——データディレクトリを named volume にする、Postgres の
`PGDATA` をサブディレクトリへ移す——を `compose.opossum.yaml` に書き出し、それを
マージした状態でプロジェクトを起動します。**あなたの compose ファイルは変更しません。**
39% → 50% の差はここから来ていて、これをやる道具は他にありません。

**3. 何も動いていないときは、何も動いていない。** Docker Desktop は常駐の Linux VM を
1つ抱えますが、Apple `container` はコンテナごとに VM を与え、待機時には1つも抱えません。
1台の Mac での実測（macOS 26・Apple silicon、`container` 1.0.0 vs Docker Engine 29.5.3）：

| | Docker Desktop | Apple `container`（opossum） |
|---|---|---|
| アイドル時のメモリ | ホストプロセス ~373 MB **＋ 常駐 Linux VM に ~7.8 GB** | ヘルパ **~58 MB**、**常駐 VM なし** |
| コンテナ1個の起動 | **~0.19 秒** | ~0.81 秒 |
| 分離 | VM カーネル共有 | **コンテナごとに VM** |
| ライセンス | 大きな組織では有償 | オープンソース・不要 |

コンテナ1個の起動は Docker のほうが速いです——VM が既に温まっているので。opossum は
「入れっぱなしにしておくのが軽いほう」です。[方法と注意点（英語）→](docs/benchmarks.md)

## 必要環境

- Apple silicon の macOS 26 以降
- [`container`](https://github.com/apple/container) がインストール済みで起動済み（`container system start`）、かつ `PATH` 上にあること — `container` 1.2.2 で検証済み
- Go 1.25 以降（ソースからビルドする場合）

## インストール

### Homebrew

```sh
brew install suruseas/opossum/opossum
```

ビルド済みバイナリが入るので、Go ツールチェインもローカルビルドも不要です。依存として Apple の `container` ランタイムも一緒に入ります。タグ付きリリースごとに更新され、ランタイムが Apple silicon の macOS 26 を要求する関係で `darwin/arm64` のみの提供です。

### ソースから

```sh
make build   # バージョンを埋め込んで ./opossum をビルド
# make を使わない場合（素の go build だとバージョンが dev になります）：
go build -ldflags "-X main.version=$(git describe --tags --always | sed 's/^v//')" -o opossum ./cmd/opossum
# ビルドしたら PATH の通った場所へ。例：
mv opossum /usr/local/bin/
```

## セットアップ（初回のみ）

サービスディスカバリには、システムに登録したローカル DNS ドメインが必要です。一度作成すれば、再起動後もそのまま残ります：

```sh
sudo container system dns create opossum
```

別の名前を使いたい場合は、その名前で作成して `--dns-domain <name>` を指定します。不要になったら `sudo container system dns delete opossum` で削除できます。

## クイックスタート（`docker compose` から来た人向け）

opossum は既存の `compose.yaml` / `docker-compose.yml` を **そのまま** 読みます。変換も専用ファイルも要りません。Docker Desktop からの乗り換えなら、イメージはすでにビルド済みのはずなので、それを再利用して Apple のビルダー（コールドスタートで遅い）を回避するのが最速です：

```sh
# 初回のみ：Apple container を起動し、サービスが名前解決できるよう
# ローカル DNS ドメインを登録
container system start
sudo container system dns create opossum

cd path/to/your-project
docker compose build       # まだビルドしていなければ（イメージがあれば不要）
opossum up --from-docker-compose   # ビルド済みイメージを Docker から取り込んで起動
```

ビルド済みイメージの名前は `docker compose` も opossum も同じ `<project>-<service>` 形式で、プロジェクト名の既定はどちらもディレクトリ名です。そのため取り込んだイメージがそのまま `up` で使われます。ただしディレクトリ名に `_` や `.` が含まれると2つのツールで正規化が食い違うので、その場合は両方のコマンドに **同じ** `-p <name>` を渡してください。

これだけで、同じプロジェクトが Apple `container` の上で動きます。操作コマンドも見慣れたものです：

```sh
opossum ps            # サービス / IP / ポート / 状態
opossum stats         # サービスごとのライブ CPU / メモリ / net / I/O
opossum logs web -f   # サービスのログを追う
opossum exec -it web sh
opossum down          # 停止＋削除（-v で named volume も削除）
```

**Apple のビルダーでビルドしたい場合は**、`--from-docker-compose` を外して `opossum up` を実行すれば、opossum が `build:` を持つサービスを自前でビルドします（重いビルドには時間がかかることがあります。[ビルドのトラブルシュート（英語）](docs/troubleshooting.md#troubleshooting-builds)参照）。どちらの場合も、先に `opossum config` を実行すると、補間を解決した後の設定と、opossum が無視するフィールド（`dns_search`、`container_name` など）を確認できます。

**サービスが起動しないときは**、よくある原因は `up` の時点で警告として表示され、対処も併記されます——DNS ドメインの未登録、Postgres のデータが named volume 直下、ホストポートの使用中（macOS で 5000/7000 が埋まっているのはたいてい AirPlay レシーバー）、`/private/tmp` 配下のビルドコンテキスト。
[それぞれの対処（英語）→](docs/troubleshooting.md)

### きれいに消すには

新しいものを試すなら、元に戻せることが前提です。撤収には3段あります。

```sh
opossum down               # 日常：コンテナとネットワークを停止・削除
opossum destroy            # このプロジェクトを跡形なく：コンテナ・ボリューム・イメージ・生成物
opossum destroy --dry-run  # 何が消えるかだけ見る（何も消さない）
```

`destroy` は、そのプロジェクトについて opossum が作ったものをすべて削除します——コンテナ（孤児を含む）、プロジェクトネットワーク、named volume、ビルド／pull したイメージ、再起動の監視プロセス、`.opossum/` 状態ディレクトリ、生成された `compose.opossum.yaml`。実行前に一覧を出して確認します。スクリプトやエージェント用に `--force` で確認を省けます。

**あなたのファイルには一切触れません**——compose ファイル・`.env`・ソースはそのままです。共有されているものにも触れません：`external: true` のボリューム、他プロジェクトのコンテナ、そしてマシン全体で共有される次の2つ（片方は `sudo` が必要で、もう片方は無関係なプロジェクトまで遅くするため、`destroy` は消し方を示すだけです）。

```sh
sudo container system dns delete opossum                  # ローカル DNS ドメイン
container builder delete --force && container image prune -a  # ビルドキャッシュと未使用イメージ
```

## 使い方

```sh
opossum up                 # ビルド＋全サービス起動（デタッチ）
opossum up web             # web とその依存だけ起動
opossum up web --foreground  # 単一サービスをフォアグラウンドでアタッチ実行
opossum ps                 # サービス / コンテナ / IP / ポート / 状態
opossum logs               # 全サービスのログ
opossum logs web --follow  # 1サービスのログを追う（-n N で tail 行数）
opossum stats              # サービスごとのライブ CPU/メモリ/net/IO（--no-stream で1回だけ）
opossum exec web ls -la    # 実行中サービスでコマンド実行
opossum exec -it web sh    # サービスで対話シェル
opossum run --rm web sh    # サービス定義から使い捨てのワンオフコンテナ
opossum stop [service...]  # 削除せずに停止
opossum restart [service…] # その場で停止して再起動
opossum down               # サービスとネットワークを停止＋削除

opossum -f path/to/compose.yaml up      # compose ファイルを指定
opossum -p myproj up                     # プロジェクト名を上書き
```

`-f` を省くと、opossum は作業ディレクトリの compose ファイルを docker-compose と同じ優先順（`compose.yaml`、`compose.yml`、`docker-compose.yaml`、`docker-compose.yml`）で探します。手元の `docker-compose.yml` はそのまま動きます。

同梱の例（ビルド不要の `hello.yaml` とフル機能の `compose.yaml`）で試せます。各サブコマンドの実例は [`examples/README.md`](examples/README.md) にあります：

```sh
cd examples
opossum -f hello.yaml up
opossum -f hello.yaml ps
```

例の `web` サービスは起動時に `db` と `cache` の解決済み IP を表示するので、名前ベースのサービスディスカバリが動いていることを確認できます。

## AI エージェントから使う

エージェントに [`AGENTS.md`](AGENTS.md) を読ませて、そのまま質問してください。
コンテキストウィンドウに載せる前提で書いた事実のみのリファレンスで、コマンド一覧、
opossum が扱う／無視する／拒否する compose フィールドの全て、失敗シグネチャと対処の
対応表が入っています。「このフィールドは効くのか」「なぜ失敗したのか」といった細かい
互換性の質問は、README の一節よりも、そのファイルを読んだエージェントのほうが
正確に答えます。

## ドキュメント

詳細ドキュメントは英語のみです（この日本語 README は英語版 README のみの翻訳です）。

| | |
|---|---|
| [Compatibility](docs/compatibility.md) | 実測とその方法、compose フィールドとコマンドの全一覧、`docker compose` との挙動差、Docker でビルドしたイメージの再利用 |
| [Networking model](docs/networking.md) | サービス同士の到達方法、複数プロジェクトの同時実行、ホストからの到達 |
| [Troubleshooting](docs/troubleshooting.md) | `container` でのビルド失敗と、踏む前に知っておく価値のある制限 |
| [Benchmarks](docs/benchmarks.md) | アイドル時のコスト、起動時間、`stats --host` が測っているもの |
| [vs Docker Desktop](docs/vs-docker-desktop.md) | 実測の横並び比較：アイドル時の占有、使い捨てコンテナの速度、ビルド、ディスク、日常運用で足りないもの |
| [MCP servers](docs/mcp.md) | エージェントに専用 VM のツールサーバーを与える |
| [Agent sandboxes](docs/agent-sandbox.md) | 信頼しきれないものを、外に出る経路なしで動かす |
| [`AGENTS.md`](AGENTS.md) | 同じ事実を、AI エージェント向けに |
| [`CHANGELOG.md`](CHANGELOG.md) · [`examples/`](examples/README.md) | 変更履歴と、各サブコマンドの実例 |

## できないこと

正直に、1か所にまとめます。opossum は **macOS 26** が必要です（コンテナ間の
ネットワークがそれに依存します——macOS 15 までこの種のオーケストレーションは
成立しません）。実装しているのは Compose 仕様のサブセットで、フィールドが
プロジェクトの意味を変えてしまう場合は推測せず拒否します——`docker.sock` の
マウントには等価物が無いため、起動前に拒否します。named volume は同時に1つの
稼働コンテナにしか接続できませんが、これは Apple `container` の制約であって
選択ではありません。`resources.limits` を超える Swarm/`deploy`、`configs`、
`extends` は無視され、`opossum config` がファイル内のどのフィールドを飛ばしたか
を教えます。

[詳細と、それぞれへの対処（英語）→](docs/troubleshooting.md)

## 開発

```sh
go test ./...

# fake shim を使うと、実ランタイムなしでオーケストレーションをスモークテストできます：
OPOSSUM_CONTAINER_BIN="$PWD/testdata/fake-container.sh" \
  go run ./cmd/opossum -f examples/compose.yaml up
```

ユーザに見える変更は `CHANGELOG.md` を直接編集せず、[`changelog.d/`](changelog.d/README.md) に変更ごとのファイルを 1 つ足す形で記録します。詳しくは [CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。

`OPOSSUM_CONTAINER_BIN` は、ランタイムとして呼び出すバイナリを上書きします。fake shim の出力は実際の CLI と同期を保っています（[`testdata/real-cli-output.md`](testdata/real-cli-output.md) 参照）。

実機の `container` で検証する際の再現可能な手順（前提・ステップ・既知の落とし穴）は [`docs/real-runtime-review.md`](docs/real-runtime-review.md) にまとめてあります。

## opossum の開発体制

opossum は主に自律型の AI コーディングエージェントが開発しています。エージェントが計画・実装・テスト・レビュー・マージまでを行い、人間が方向性を決めてリリースを承認します。

## ライセンス

[MIT](LICENSE) © opossum contributors.

## 商標

「Apple」と「`container`」は互換性を示すために記述的に言及しています。「Docker」と「Docker Compose」は Docker, Inc. の商標です。opossum はいずれとも提携・後援関係にありません。
