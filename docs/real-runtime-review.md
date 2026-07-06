# 実ランタイム・スプリントレビュー手順

opossum は二層で検証する:

| 層 | 手段 | 確かめること | 頻度 |
|---|---|---|---|
| 日々のカイゼン | fake シム | ロジック（パース・順序・引数組み立て・失敗系） | 毎回 |
| スプリントレビュー | 実 `container`（macOS 26 の実機） | 現実環境で本当に動くか | 要所 |

「fake で論理的に正しい → 実ランタイムで現実でも動く」の二段で前進を確かめる。
この文書は後者（実機レビュー）の**再現可能な手順**をまとめる。M1〜M3 のレビューで
環境状態に依存して結果がブレた反省（builder が起動する/しない 等）から明文化した。

## 日々（fake シム）

実ランタイム無しで、発行される `container` コマンド列を高速・無人で検証する。

```sh
go test ./...        # 回帰ゲート

# end-to-end スモーク（発行コマンドは $FAKE_LOG に記録される）
FAKE_LOG=/tmp/opossum-fake.log \
OPOSSUM_CONTAINER_BIN="$PWD/testdata/fake-container.sh" \
  go run ./cmd/opossum -f examples/compose.yaml up
cat /tmp/opossum-fake.log   # 実際に叩かれた container 呼び出し
```

fake シムの出力は実 CLI 1.0.0 に合わせてある（`testdata/real-cli-output.md` が golden）。
CLI を更新したら golden を採り直して同期すること（Issue #10 の経緯）。

## スプリントレビュー前チェックリスト（実機）

```sh
container --version                 # 1.0.0 を想定
container system status             # status: running であること
container system dns list           # 'opossum' ドメインがあること（無ければ下記で作成）
```

- **DNS ドメイン**（サービスの bare-name 解決に必要。一度だけ・再起動後も永続）:
  ```sh
  sudo container system dns create opossum
  ```
- **builder**（`build:` を持つサービスを使う場合のみ必要）:
  ```sh
  container builder status
  container builder start            # 未起動なら
  ```
  builder は起動に失敗することがある（下記「既知の落とし穴」）。**image のみのサービスは
  builder 不要**なので、builder が不調なときは image ベースの構成でレビューする。

## レビュー手順

コンテナ名は `<service>.<project>.<domain>`（例 `web.hello.opossum`）。**コマンド表面を
一通り見るなら、builder 非依存の `hello.yaml`** を使うのが安定（`examples/README.md` の
ウォークスルーと対応）。

```sh
cd examples

# 1) 起動（image は pull。build サービスは builder が要る）
go run ../cmd/opossum -f hello.yaml up

# 2) 状態：SERVICE/CONTAINER/IMAGE/IP/PORTS/STATUS
go run ../cmd/opossum -f hello.yaml ps

# 3) ログ（-f で追従、-n N で末尾）。名前解決も実機で確認
go run ../cmd/opossum -f hello.yaml logs web
container exec web.hello.opossum nslookup db     # bare name で db へ解決するか

# 4) 部分起動・ライフサイクル
go run ../cmd/opossum -f hello.yaml up web        # web と依存のみ
go run ../cmd/opossum -f hello.yaml stop          # 削除せず停止
go run ../cmd/opossum -f hello.yaml restart       # その場で stop→start

# 5) 後片付け（逆順で stop/delete → network 削除）
go run ../cmd/opossum -f hello.yaml down

# 6) 残骸ゼロを確認（レビューの一部）
container ls -a | grep hello.opossum   || echo "no hello containers"
container network list | grep hello-net || echo "hello-net removed"
```

**build / health / completed の経路**は `compose.yaml` で確認（`container builder start` が要る）:

```sh
container builder start
go run ../cmd/opossum -f compose.yaml up     # db/cache healthy 待ち → migrate 完走 → web build/起動
go run ../cmd/opossum -f compose.yaml down
```

確認観点の例:
- `up`: network 作成 → 依存順に起動。`service_healthy` 依存は「Waiting for ... to be healthy」の後に
  起動、`service_completed_successfully` の migrate は前景で完走してから web が起動（#M2 / #5）。
  途中で失敗したら起動済みコンテナと作成した network をロールバックし残骸を残さない（#28）。
- `ps`: 実 `inspect` の `status.state` を STATUS に、`publishedPorts` を PORTS に表示。公開ポートの
  `0.0.0.0` を IP と誤認しない（#M3）。
- `logs` / `stop` / `restart` / `up <svc>`: 発行される `container logs|stop|start` と対象が正しいか。
- 複数プロジェクト: `-p a` と `-p b` で同時起動でき、同じ service 名でも衝突しない（#9）。
- `down`: 逆順 teardown、`<project>-net` 削除。既に無い network への再 `down` は警告を出さない（#10）。

## ドッグフーディング価値検証（記録）

Homebrew 公開ゲート「十分な価値検証」の証跡。代表的な実 compose を実機（container 1.0.0 / macOS 26）で
end-to-end に回し、動作と穴を実測した記録を残す。

- **2026-07-03 — 単一フルスタック（web+db+cache+worker）**: version/networks/named volume/env_file/
  healthcheck/depends_on condition/container_name/restart を網羅した代表 compose を実機で検証。動いた:
  探索 as-is 起動・config 解決/検証・health-gating・discovery（worker が db/cache を bare 名解決）・
  published port が host から到達・rollback（#28）・down クリーン。**最大の発見**: DB+named volume が
  実 container で失敗（→「既知の落とし穴」）。派生バックログ #56-#59 を起票・対応。
- **2026-07-04 — 広域 multi-project ＋ build-from-source（最優先）**: 同一の service 名
  （db/web/builder）を持つ2プロジェクト `shopapi`/`blog` を**並行起動**し、以下を実機で実証:
  - **discovery がプロジェクト内に閉じる（#9 の discovery 面）**: `blog` の web が bare `db` を
    `db.blog.opossum`（192.168.67.3＝自分の db）に解決し、`shopapi` の db（192.168.66.3）には漏れない。
    ネットワークも `shopapi`=192.168.66.x / `blog`=192.168.67.x で分離。
  - **build-from-source（`build:`）**: 各プロジェクトが `shopapi-builder:latest` / `blog-builder:latest`
    を Dockerfile から個別にビルドして起動（ログに `built-from-source-...`）。
  - **named volume 分離（#63）**: 同名 volume `shared` が `shopapi_shared`/`blog_shared` に分離自動作成、
    `down -v` は呼び出し側のみ削除し他プロジェクトのデータを残す（footgun 修正の実機確認）。
  - いずれも down で残骸ゼロ。**新たな穴は出ず**、複数プロジェクト分離が名前(#9)＋network＋volume(#63)＋
    discovery の4面すべてで完成していることを確認。
- **2026-07-04（続き）— 核心プロパティの実機確認**（公開前の breadth 積み増し。いずれも新たな穴なし）:
  - **実 TCP/HTTP 疎通**: nginx server ＋ alpine client で、client が `wget http://server/` により**bare 名越しに**
    nginx の "Welcome to nginx!" を取得（EXIT=0）。DNS 解決だけでなく実際の TCP 接続＋HTTP 応答が compose 流の
    bare service 名で成立＝看板機能の核心。
  - **named volume のデータ永続化**: `data:/data` に marker を書いた後 `down`（-v なし）→ 再 `up` で同一データを
    読み戻せる（`persist_data` が保持され、コンテナ再作成を跨いでデータが残る）。ステートフルの実用要件を確認。
  - **依存順序の二重ゲート**: `db`(healthcheck) → `migrate`(service_completed_successfully) → `app` の構成で、
    `Waiting for db to be healthy` → `Running migrate to completion`（前景 exit 0）→ `Starting app` の順に
    実行されることを実機で確認。`ps` は完走した one-shot `migrate` を `stopped` と正しく表示。
- **2026-07-04（capstone）— 実在アプリ end-to-end（postgres バックエンド）**: `db`(postgres:16) ＋ `app`(psql)
  の実スタックを実機で通し、**現実の全レイヤ**を一度に裏付けた（新たな穴なし）:
  - **実 DB を named volume で初期化（PGDATA 回避）**: `pgdata:/var/lib/postgresql/data` ＋
    `PGDATA=/var/lib/postgresql/data/pgdata` で initdb 成功、`database system is ready to accept connections`。
    「既知の落とし穴」の回避策が実アプリで有効なことを確認。
  - **health-gate（pg_isready）→ bare 名で実 SQL**: `healthcheck: pg_isready` で db healthy を待ち、
    `app` が `psql -h db`（bare 名）で `CREATE TABLE`/`INSERT`/`SELECT` を実行し `APP-QUERY-OK`。
  - **実 DB データの永続化**: `down`（-v なし）→ 再 `up` でテーブルが残り（`relation "t" already exists`）
    行数が 1→2 に増える。`opossum exec db psql -c 'SELECT count(*)'` = 2 で確定（exec も実サービスで動作）。
  - `down -v` で `capstone_pgdata` 含め残骸ゼロ。

## 実在 docker-compose.yml 互換性検証（#72）

opossum 自身が書いた compose ではなく、**他リポジトリの本物の docker-compose.yml をそのまま**回して
現実の互換性を測る（段階を上げていく）。各 rung の結果を追記する。

- **rung 1（2026-07-04）— `docker/awesome-compose` の `wordpress-mysql/compose.yaml`（無改変）**:
  取得 `curl raw.githubusercontent.com/docker/awesome-compose/master/wordpress-mysql/compose.yaml`。
  `mariadb:10.6.4-focal` ＋ `wordpress:latest`、`expose`/`restart: always`/top-level `volumes:` を含む。
  **結果: as-is で end-to-end 動作**（新たなバグなし）。
  - `config`/`up` が未対応フィールドを明示警告（`(top-level): volumes` / `db: expose, restart` /
    `wordpress: restart`）してクラッシュせず起動。
  - `db_data:/var/lib/mysql` は `wpdemo_db_data` に名前空間化され自動作成。**MariaDB は named volume の
    datadir で正常初期化**（`mysqld: ready for connections.`）。
  - wordpress が `WORDPRESS_DB_HOST=db`（bare 名）で DB に接続、`ports: 80:80` 公開でホスト
    `localhost:80` が **302 → `/wp-admin/install.php`**、追従すると本物の「WordPress › Installation」画面
    ＝DB 接続まで成立。`down -v` で残骸ゼロ。
  - **発見（バグでなく知見）**: named volume の datadir に DB を載せる制約は **Postgres 固有**（`initdb` が
    非空ディレクトリを拒否）で、**MariaDB/MySQL は問題なく初期化できる**（下の「既知の落とし穴」に反映）。
- **rung 2（2026-07-04）— `docker/awesome-compose` の `nginx-golang-postgres`（無改変, build 含む多層）**:
  awesome-compose を shallow clone し、`nginx(proxy)` ＋ `go backend(build)` ＋ `postgres` の3層をそのまま検証。
  **結果: 実在 compose 頻出の未対応記法を3件発見**（as-is では動かず → いずれも issue 化し後続スプリントで対応）。
  - **[#74 高] long-form volume 構文でパース失敗**: `- {type: bind, source, target, read_only}` を
    `cannot unmarshal !!map into string` で拒否（proxy の nginx.conf マウント）。→ その compose は起動不能。
  - **[#75 高] `build.target` が黙って無視される**: `Build` に Target が無く、Service と違い未知キーを
    Unsupported に記録しないため警告も出ず、マルチステージの意図したステージでビルドされない。
    awesome-compose 39 ファイル中 **16** が使用。
  - **[#76 中] `secrets` 未対応**: `POSTGRES_PASSWORD_FILE=/run/secrets/db-password` 等の `_FILE` パターンが
    secret 未マウントで機能しない（39 中 8 が secrets 使用）。
  - **数量的把握**（awesome-compose 39 ファイル）: build.target=16, secrets=8, long-form volume=3, configs/env_file=0,
    build なし（pre-built のみ）=12。→ **as-is 互換の主障壁は build.target と long-form volume**。優先実装対象。
- **rung 2 再検証（2026-07-04, #74/#75 実装後）**:
  - **[#74/#75 解消確認]** `nginx-golang-postgres` を再度 `config` すると long-form volume が
    `./proxy/nginx.conf:/etc/nginx/conf.d/default.conf:ro` に正規化され、`build.target: builder` も表示され、
    **完全描画**（以前は long-form volume で parse 不能だった）。build 時に `container build --target builder` が
    実機に届き builder ステージがビルドされることも確認。→ 実在 compose の**主障壁だった 2 件は解消**。
  - **[#81 訂正済み — build 系も home 配下で完動]** 当初 build 時の `COPY` が
    `failed to calculate checksum ... "/<file>": not found` で失敗し「builder が COPY 不能」と誤結論したが、
    チーム独立検証＋精密再現で**誤りと判明**。原因は検証を **scratchpad（`/private/tmp/...`）** で行っていたこと。
    最小再現（2行 Dockerfile・直 `container build`）で場所だけ変えると: **home（`/Users/...`）成功／`/tmp` 成功／
    `/private/tmp`（real temp）失敗**。Apple builder VM は home と `/tmp` を mount するが `/private/tmp` を mount
    しないため。**realpath 強制解決は逆効果**（`/tmp/x`→`/private/tmp/x` で成功→失敗）。
  - **[build 系 end-to-end 実証]** `docker/awesome-compose` の `nginx-golang`（**build.target ＋ long-form volume**
    使用）を `$HOME` 配下にコピーして `opossum up`: backend の **go build 成功**、long-form volume 正規化・
    `build.target: builder` 適用、backend/proxy 起動、`curl localhost:80` が nginx→go backend 経由で応答、
    `down` で残骸ゼロ。→ **実ユーザーの home 配下プロジェクトは build 系も完動**。「build 系 full run 不可」の
    公開懸念は解消。builder 非対応の場所/symlink への配慮は enhancement #83 で検討。
- **rung 3（2026-07-04）— 実 Rails 7 repo（`ryanwi/rails7-on-docker` の `compose.yaml` 無改変）**:
  `web`(build: custom `development.Dockerfile`)＋`db`(postgres:17)＋`redis` の3層。`$HOME` 配下で検証。
  - **parse 完全**: custom dockerfile 指定・`command` の `bash -c` shell-split・**bind＋named volume 混在**
    （`.:/usr/src/app` ＋ `bundle:/usr/local/bundle`）・`env_file`・**depends_on の条件混在**（db=healthy /
    redis=started）・healthcheck を正しく解決。
  - **env_file 欠如の挙動**: repo は `.env` を gitignore（`.env.example` も無し）。opossum は `env_file ".env" not
    found` で**明確にエラー**（docker compose 準拠の正しい挙動）。`.env` を用意すれば進行。→ 長形式
    `env_file: {required: false}` 未対応は #85（低）。
  - **build 駆動 正常**: `opossum build web` が `ruby:3.3.9-slim` ベース＋`-f development.Dockerfile` で build を
    駆動、context 転送・apt/bundle 進行を確認（opossum の責務は完遂。bundle install 完了は待たず停止）。
  - **postgres named-volume 制約 #57 に的中**: `db` の `pg_data:/var/lib/postgresql/data`（PGDATA 回避なし）で
    `initdb: error: directory ... exists but is not empty ... lost+found ... Create a subdirectory` ＝db が
    healthy にならず、full `up` は #56 の明確なエラーで rollback。**opossum のバグではなく既知の実制約**
    （PGDATA サブディレクトリ回避で解消可）。
- **rung 4（2026-07-04）— 実 Vite+Vue repo（`pdpfsug/dev-vuejs-docker-compose` 無改変）**:
  単一 `vue`(build: `vuejs`, `command: pnpm dev --host`, ports 5173, bind volume, legacy `version: '3.7'`)。
  - **parse 完全**: build context・`pnpm dev --host` の shell-split・ports・bind volume を解決、legacy
    `version` は no-op として警告なし（正しい）。
  - **build 駆動 正常**: `$HOME` 配下で node build を駆動（context 転送・base image・RUN 実行）。ただし build は
    repo の Dockerfile 内 `RUN pnpm setup` で失敗（`ERR_UNKNOWN_BUILTIN_MODULE: node:sqlite`＝pnpm 11.9.0 と
    base node の不整合）＝**repo の Dockerfile 側の問題で opossum 無関係**（opossum は失敗を exit 1 で伝播）。
  - **追検証（2026-07-06）— クリーンな Vite+Vue で end-to-end 完動を確認**: 上記は repo Dockerfile の陳腐化で
    serve まで至らなかったため、最小の実 Vite+Vue アプリ（`vue@3`＋`@vitejs/plugin-vue`＋`vite@5`, `node:20-alpine`,
    `npm install`→`vite --host`）を `$HOME` 配下で `opossum up`:
    - build 成功（`vitevue-web:latest`）、web running、`ports: 5173` 公開。
    - web ログ `VITE v5.4.21 ready in 184 ms` / `Local: http://localhost:5173/`。
    - `curl localhost:5173` が Vite の index HTML（`/@vite/client`＋`/src/main.js`）を配信、`/src/main.js` は Vite
      変換済みモジュール（`import ... "/node_modules/.vite/deps/vue.js"`, `import App from "/src/App.vue"`）＝
      **Vue 解決＋Vite 依存プリバンドル＋dev server が実機で完動**。残骸ゼロ。
    - → **Vite+Vue パターンは opossum で完全に動く**。#72 rung4 の失敗は *repo の Dockerfile の陳腐化* が原因で、
      opossum/Vite/Vue 側の問題ではないことを確定。

### #72 総括（実在 compose 互換性）
- **opossum は多様な実 docker-compose.yml を正しく parse し、home 配下から build を駆動する**（simple→Rails→Vite で確認）。
- **full run の可否は compose/repo 側に依存**: pre-built（wordpress-mysql）と clean-build（nginx-golang）は完動。
  DB-on-named-volume（Rails の postgres）は #57 の PGDATA 回避が要る。一部 repo は Dockerfile 自体が壊れている
  （Vite の pnpm/node 不整合）。
- 実装で潰した実在 compose の主障壁: **#74 long-form volume / #75 build.target**。残る既知: #57（postgres）・
  #76（secrets）・#83（builder 非対応の場所）・#85（env_file required:false）。

## 実在 compose 広域検証 #97（pre-built スタックの多様性）

#72 の続き（ユーザ要望）。種類を広げて他リポジトリの pre-built docker-compose.yml を `$HOME` 配下で実機検証。

- **prometheus-grafana（`docker/awesome-compose`, 監視, 無改変）**: prometheus＋grafana。**config bind mount＋named
  volume＋command args＋container_name/restart 無視** を検証。
  - **config bind mount が実 compose で機能**: prometheus が `./prometheus:/etc/prometheus` 越しに
    `prometheus.yml` を読めた（grafana も datasources 読込）。named volume は `promgraf_prom_data` に名前空間化。
  - grafana は `localhost:3000` → 302 /login で**稼働**。
  - **prometheus は stopped**＝repo の `prometheus.yml` が古く（Alertmanager api v1）**最新 `prom/prometheus` image が
    拒否**したため（`expected Alertmanager api version to be one of [v2]`）。**opossum 無関係の repo config 陳腐化**
    （unpinned `image: latest` ＋ 古い config の"compose rot"）。opossum は正しく実行し stopped を正報告。
- **postgresql-pgadmin（`docker/awesome-compose`, DB＋管理UI, 無改変）**: **as-is で end-to-end 完動**。
  - **`.env` の `${VAR}` 補間が機能**: `POSTGRES_USER`/`PGADMIN_DEFAULT_EMAIL` 等が `.env` から解決。
  - pgadmin は `localhost:5050` → 302 /login で**稼働**、postgres は `opossum exec postgres pg_isready` で
    `accepting connections`。postgres は volume 未使用のため #57 の PGDATA 問題も出ない。残骸ゼロ。
- **知見**: pre-built 実 compose は opossum で概ね完動（config bind mount・named volume 名前空間化・`.env` 補間・
  published port・exec すべて実機で確認）。動かない場合は **repo 側の陳腐化**（unpinned image と古い config の不整合）
  が主因で、opossum のバグではない。

### #97 第2弾（realistic app ＋ runtime 機能ギャップ, 2026-07-06）

- **gitea-postgres（`docker/awesome-compose`, 自己ホスト git, 無改変）**: gitea＋postgres。**#57（postgres
  named-volume）に的中する現実例**。db が `db_data:/var/lib/postgresql/data` を使い initdb が mount point で失敗→
  **db stopped**（opossum は stopped を正報告）。gitea 自体は起動し `localhost:3000` → HTTP 200 で web 応答するが、
  db 無しでは非機能。→ **gitea/nextcloud 等の自己ホスト app compose は postgres データを named volume に直載せする
  ことが多く #57 に当たる**。回避は PGDATA サブディレクトリ（→ opossum 側の警告 ergonomics を検討: 別 issue）。
- **wireguard（`docker/awesome-compose`, VPN, 無改変）**: **Apple container の runtime 機能ギャップの例**。
  - opossum が **`cap_add` / `sysctls` / `container_name` / `restart` を無視警告**（surfaced、silent でない）。
  - up は `/usr/share/appdata/wireguard/config` 等 **Linux ホスト前提の bind mount が存在せず**失敗（明確にエラー
    伝播・stopped 正報告）。加えて `NET_ADMIN`/`SYS_MODULE` cap やカーネルモジュール（`/lib/modules`）は Apple
    container で提供されない。→ **カーネル機能/特権 cap/Linux ホストパスに依存する compose は Mac（Apple container）
    では動かない**（Docker Desktop on Mac でも同種の制約）。opossum は未対応フィールドを警告し失敗を正しく伝える。
- **portainer（inspection）**: `command: -H unix:///var/run/docker.sock` ＋ `/var/run/docker.sock` の bind mount＝
  **Docker socket 前提**で、Apple container にはアーキ的に非適用（docker socket が無い）。
- **知見（第2弾）**: opossum は「動かせないもの」も**誠実に扱う** — 未対応 runtime フィールド（cap_add/sysctls/
  devices/privileged）は警告し、存在しない host パスの bind は明確にエラー伝播。動かない主因は (a) #57 の
  postgres named-volume（頻出・要 PGDATA 回避）、(b) Linux カーネル/特権/docker-socket 依存（Apple container の
  範囲外）で、いずれも opossum のバグではない。
- **nextcloud-postgres（`docker/awesome-compose`, クラウドストレージ, 無改変）**: nextcloud＋postgres。gitea 同様
  db が `db_data:/var/lib/postgresql/data` を使い **#57 に的中→db stopped**。nextcloud web は `localhost:80` → 200。
  **#103（datadir 警告）を実 app で実証**: up 時に `warning: service "db": a named volume mounted at
  /var/lib/postgresql/data will fail Postgres initdb ... PGDATA=.../pgdata (#57)` が発火（実 app で回避を誘導できる
  ことを確認）。→ #103 の ergonomics が gitea/nextcloud 系の頻出パターンで有効。

## 既知の落とし穴

- **Postgres の data ディレクトリを named volume に直接載せると起動失敗する**（ドッグフーディング検証 #55 で確認）。
  named volume は `lost+found` を含む mount point としてマウントされ、**postgres の `initdb`** が
  「非空のデータディレクトリ」を拒否する（`directory ... exists but is not empty ... using a mount point
  directly ... not recommended`）。回避: **mount のサブディレクトリ**を使う
  （`PGDATA=/var/lib/postgresql/data/pgdata`）。この場合 `up` は health-gate で never-healthy を検知し
  `container is not running ... check opossum logs <svc>` を返して rollback する（#56）。
  **※ MariaDB/MySQL はこの制約を受けない**（datadir が空でなくても既存 DB を検出して初期化を続けるため）。
  実在の `wordpress-mysql` compose（#72 rung 1）が `db_data:/var/lib/mysql` を無改変で正常起動することを確認済み。
- **builder が `StreamClosed / HTTP2 ProtocolError` で起動しないことがある**。`build:` サービスの
  ビルドが `Timeout waiting for connection to builder` で失敗する。opossum は失敗を exit 1 で
  正しく伝播する。回避: builder 非依存の `hello.yaml` でレビューするか、builder を起動し直す。
- **restart で IP が再割当される**。`container start` が新しい IP を振るため。コンテナ名・設定は
  保たれ、名前解決（DNS 再登録）には影響しないが、IP を直接握っている外部クライアントは注意（#33）。
- **`gh pr create/edit` が org スコープ不足で GraphQL エラー**になる環境がある。PR の作成/編集は
  `gh api -X POST/PATCH repos/<owner>/<repo>/pulls`（本文は `-F body=@file`）で回避する。

## ES 7.x（elasticsearch-logstash-kibana）— cgroup NPE で不可（2026-07-06 実機）
awesome-compose/elasticsearch-logstash-kibana をユーザが検証。**elasticsearch が起動直後にクラッシュ**（`opossum ps` が
`stopped` 表示→原因特定に寄与）。ログ:
```
Exception in thread "main" java.lang.NullPointerException:
  Cannot invoke "jdk.internal.platform.CgroupInfo.getMountPoint()" because "anyController" is null
    at org.elasticsearch.tools.launchers.DefaultSystemMemoryInfo.<init>
```
= ES 同梱 JDK が起動時にヒープ量を cgroup から読む際、Apple container VM の cgroup マウントが期待形でなく NPE。
**ヒープ明示（ES_JAVA_OPTS=-Xms512m -Xmx512m, compose に既存）より前**の段階で発生し回避不可。`JAVA_TOOL_OPTIONS=
-XX:-UseContainerSupport` は ES がセキュリティ上無視。**7.16.1・7.17.0 の両方で再現**。→ ランタイム/JDK–VM 非互換で
opossum 無関係。Kibana は ES 依存のため localhost:5601 も非機能（ES ダウンが根本）。README「Won't run」に記載。
