---
title: "Apple container が 1.0 に。今あなたの開発環境を移すべきか、実測してみた"
emoji: "🦝"
type: "tech"
topics: ["docker", "macos", "container", "applesilicon", "dockercompose"]
published: false
---

Apple 純正の [`container`](https://github.com/apple/container) ランタイムが6月に **1.0** に到達しました。macOS 26 ではコンテナ間通信と名前解決が使えるようになり、複数サービスの開発スタックを動かす最後のピースが揃いました。ピースは、本当にもう全部あります。

そこで正直に知りたかったのは「**今日、日常の開発環境をこれに移すべきか？**」です。それを決めるのは2つ——**互換性**（手元の `docker-compose.yml` は動くか）と**パフォーマンス**（住み心地は良いか）。結論を先に、隠さず置きます。

> **互換性は本物です——試した compose の約半数は無改変で、6割は1行以内の修正で動きました。ただしパフォーマンスは現時点で明確にビハインドがあり**、普通の多サービス開発スタックではそこが効いてきます。**今すぐ** Docker Desktop の代替が欲しいなら、Docker 本体や Colima・OrbStack（いずれも Docker と同じ共有 VM モデルの既存代替）の方が実用的です。Apple container が今日勝てるのはただ一つの形——**小さなイメージを1つ2つ、たまに動かす**ケースだけ。将来性は高いので注視はしますが、メインの開発スタックを今移すのはまだ早い、というのが正直な評価です。

その裏づけとなるデータを示します。

## Part 1 — 互換性：予想より良かった

推測ではなく測りました。Docker 公式のサンプル集 [awesome-compose](https://github.com/docker/awesome-compose)（WordPress、React+Express+Mongo、Spring+Postgres、Prometheus+Grafana など）から **29本を無改変で** Apple container 上で起動し、結果を分類しました。`nextcloud-postgres` から `wordpress-mysql` までのアルファベット順スライスで、恣意的な選別はしていません。

### 「Apple container で compose」には道が複数ある

本題の前に整理を。この課題へのアプローチは大きく3つあります。

|  | Docker Desktop | container 内に docker engine を立てる | ネイティブ（本記事） |
|---|---|---|---|
| compose 互換 | 100% | 100%（本物の engine） | サブセット（実測は後述） |
| 常駐 VM | あり（数 GB を確保。実メモリは Part 2 で実測） | あり（自前の Linux VM） | **なし** |
| 分離モデル | VM 内でカーネル共有 | VM 内でカーネル共有 | **コンテナごとに VM** |
| セットアップ | インストーラ | カーネル差し替え等（重め） | brew + 2コマンド |
| ライセンス | 大規模組織は有料 | 無料 | 無料 |

2番目の「`container` の永続 VM に本物の docker engine と compose をインストールする」方式は [7kaji さんの記事](https://zenn.dev/7kaji/articles/370a8dd7f678d1)が詳しく、**フル互換が必須なら現時点で最も確実な道**です。ただしこの方式は常駐 VM を自前で持つことになるので、Apple container の「動かしていないときは軽い」「コンテナごとに VM 分離」という利点はトレードオフになります。

本記事は3番目、**ランタイムにネイティブに乗る**道の実測レポートです。

### セットアップ

Apple の `container` はランタイムであってオーケストレータではありません。`compose` サブコマンドも、依存順の起動も、サービスディスカバリもありません。その層として、筆者が開発している [opossum](https://github.com/suruseas/opossum) を使います。既存の `compose.yaml` / `docker-compose.yml` を読んで、見慣れた動詞——`up` / `ps` / `logs` / `exec` / `down`——を提供する小さなオーケストレータです。

```sh
brew install suruseas/opossum/opossum
container system start                     # ランタイム起動（初回）
sudo container system dns create opossum   # 初回のみ: サービス名で相互解決する
                                           # ためのローカル DNS ドメイン登録
```

検証方法：macOS 26 / Apple silicon / Apple container 1.0。各サンプルで `cd` して `opossum up`、起動猶予は約90秒、`ps` と `logs` を確認して分類し `down`、次へ。失敗したものは全件、**本当の原因がどこにあるか**（ランタイムか・サンプルか・環境か）を切り分けました。

### 結果

| 結果 | 件数 | 内訳 |
|---|---:|---|
| ✅ **無改変**でそのまま起動 | **14** | WordPress、React+Express+MySQL/Mongo、Spring、nginx+Go+MySQL、pgAdmin、Kafka など |
| 🔧 compose **1行**の変更で起動 | 4 | Postgres データディレクトリ×3、amd64 専用イメージ×1 |
| 🏗️ サンプル自体が**腐っていた** | 5 | どのランタイムでも失敗する |
| 🚫 **原理的に**動かない | 3 | Docker socket×2、Linux カーネル依存×1 |
| ⚙️ 環境・設定の問題 | 2 | プレースホルダパス、ホストポート衝突 |
| 🐌 ビルドがタイムアウトに間に合わず | 1 | Rust→Wasm の10〜15分ビルド（エラーなし） |

まとめると：**29本中18本（62%）が「そのまま」か「1行修正」で動き、14本は1文字も変えていません。** そして動かなかった11本のうち、ランタイムのコンテナ実行やネットワークが原因のものは **0件**でした。失敗はすべて、サンプル側の劣化・Docker 固有のホスト機能・こちらの環境要因に帰着しました。

いくつかのカテゴリは、Apple container が Docker とどう違うかを教えてくれるので、順に見ていきます。

### 1行修正の中身（ここに学びがある）

#### Postgres がデータ用 volume を拒否する（3本）

`nextcloud-postgres` などは、お決まりのこれを定義しています。

```yaml
volumes:
  - db-data:/var/lib/postgresql/data
```

Docker では動きますが、Apple container では Postgres が *"initdb: error: directory exists but is not empty"* で死にます。原因：Apple container は named volume を本物の ext4 マウントポイントとしてマウントするため、中に `lost+found` があり、`initdb` は空でないディレクトリを拒否するのです。

修正は、ベアメタルのマウントでもおなじみの1行：

```yaml
environment:
  PGDATA: /var/lib/postgresql/data/pgdata   # マウント配下のサブディレクトリを指す
```

このパターンは自己ホスト系アプリの compose（Gitea、Nextcloud …）に頻出するので、opossum は `up` 時に検知して上記の修正案を警告として表示します。MySQL / MariaDB はマウントポイントを許容するので、WordPress 系サンプルは素通りでした。

#### amd64 専用イメージ（1本）

`nginx-nodejs-redis` は x86-64 のみ公開の `redismod` を使っています。修正は Docker on Apple silicon と同じ1行：

```yaml
platform: linux/amd64
```

opossum はプラットフォーム指定をランタイムに渡し、Rosetta 変換を自動で有効にします。

### 到着前から壊れていた5本

今回の意外な発見です。5本は**どのランタイムでも**失敗します。バージョンを何も pin しておらず、世界が先に進んでしまったからです。

- `nginx-flask-mysql` — Flask バックエンドが `ImportError: cannot import name 'url_quote' from 'werkzeug.urls'` でクラッシュ。有名な Flask/Werkzeug 2.1 非互換で、サンプルが Werkzeug を pin していないのが原因（後続の nginx の "host not found in upstream" は死んだ backend の連鎖にすぎません）
- `prometheus-grafana` — pin されていない `prom/prometheus` の最新イメージが、サンプルの `api_version: v1` な Alertmanager 設定を拒否
- `nginx-wsgi-flask` / `react-rust-postgres` / `vuejs` — ビルド中の `pip install` / `cargo` / `yarn global add` が失敗。すべて依存の経年劣化

ランタイム非互換を探しに行ったら、「メンテされていない compose ファイルが数年でどうなるか」の小さな博物館を見つけた、という話です。自分のスタックがイメージと依存を pin しているなら、このカテゴリは丸ごと無関係です。

### 原理的に動かない3本（正直に）

ここがモデルの本質的な違いです。

- **`portainer` / `traefik-golang`** は `/var/run/docker.sock` を bind mount します。仕事が「Docker デーモンと話すこと」なので、Docker socket が存在しない Apple container では対象外です
- **`wireguard`** は `NET_ADMIN` とホストのカーネルモジュール（`/lib/modules`）が必要。コンテナごとに独立した micro-VM なので、届く先の「共有 Linux ホストカーネル」がありません

あなたの compose が「普通のアプリ＋DB＋キャッシュ」なら、ここには当たりません。ホストレベルのインフラツールなら Docker に残るか、冒頭で触れた[フル互換ルート](https://zenn.dev/7kaji/articles/370a8dd7f678d1)が受け皿になります。

### サンプル外で知っておくべき差分

より広い検証で見つけた、試す前に知っておくと良い差を2つ：

- **新規 volume は空でマウントされる。** Docker は新品の named/anonymous volume をイメージのその位置の内容で初期化しますが、Apple container はしません。あの `- /app/node_modules` トリックが壊れる、ということです。opossum はこの seeding を自前で再現しているので compose のパターンはそのまま動きますが、素の `container` コマンドで触ると踏む差です
- **cgroup を読む JVM がクラッシュする。** Elasticsearch 7.x 同梱の JDK はヒープサイズ決定のためにホストの cgroup を読みに行き、micro-VM が期待どおりのマウントを見せないため、設定が効く前に起動時死します。回避策は今のところ見つけていません

## Part 2 — パフォーマンス：ここが現時点でのビハインド

互換性は良いニュースでした。パフォーマンスは正直なところ悪いニュースで、そして日常で実際に効いてくるのはこちらです。以下の数値は1台（M2 / 16GB）での実測です。ぜひご自身で再現してください。大事なのは小数第3位ではなく**傾向の形**です。

**メモリはアイドル値の印象どおりには効きません。** 「アイドル ~50MB vs Docker の数 GB VM」は事実ですが、それは話の入口にすぎません。2つのランタイムはメモリの割り当て方が根本的に違うからです。Apple container は**コンテナごとに VM が1つ**で、このコストは正確に検証できます：アイドルの `nginx:alpine` VM は physical footprint **~270MB**（素の alpine で ~235〜255MB——これがゲストカーネルの固定床で、`-m` で上限を下げても下がりません）、**綺麗に線形**にスケールし（6コンテナ＝6 VM ≈ 1.6GB ＋ 各 ~20MB のヘルパー）、しかも macOS はゲストのメモリを VM プロセスにきちんと帰属させます——VM 内に圧縮不能な 300MB を保持させたら、footprint がちょうどその分増えました（254M → 557M）。

一方 Docker は**共有 VM 1つ**を全コンテナで分け合います。そして測定には、筆者自身も一度ハマった罠があります：Docker のゲストメモリは `com.docker.*` 系のプロセスには現れません。**アクティビティモニタで「Virtual Machine Service for Docker」と表示される別プロセス**（両ランタイムとも Apple の Virtualization framework を使っているため）に計上されていて、同じ 300MB テストで*その*プロセスがちょうど 0.3GB 増えます（1.1G → 1.4G）。正しく合算すると Docker はこうなります：

| | Apple container（正確・線形） | Docker Desktop（弾力的なベース） |
|---|---|---|
| 何も動かしていない | **~50MB** | ~1.4〜2.1GB（helpers + VM。長時間アイドルでのみ縮む） |
| 小さいコンテナ1個追加ごと | **+~290MB** | ほぼ **+0**（nginx を6個足しても ~10MB） |
| 重いワークロード終了後 | VM ごと解放 | VM は**高水位を保持**したまま |

つまりメモリの正直な結論は、どちらの陣営の宣伝よりも微妙なところに落ちます：アイドル〜1・2個なら Apple の圧勝。**典型的な 5〜10 サービスのスタックでは両者ほぼ互角**（Apple は正確に積み上がる ~1.5〜3GB、Docker は ~1.4〜2.1GB の弾力的なベース）。~10 サービスを超えると Docker の共有プールが明確に勝ちます。交点は Docker の VM がどれだけ温まっているかで **2〜7個の幅**を持ちます。per-VM モデルの本当の強みは分離（暴走コンテナが隣を食わない）と**予測可能性** — 各コンテナのコストをプロセス単位で指させることです。

**開発スタックの選択を実際に決めるのはメモリではなく速度です。** 同一マシン：


| | Docker Desktop | Apple container |
|---|---|---|
| コンテナ単発起動 | **~0.19秒** | ~0.81秒（micro-VM を都度ブート） |
| 10連発 `run --rm`（逐次） | **2.1秒** | 8.3秒（~4倍遅い） |
| 10連発 `run --rm`（並列） | **0.75秒** | 7.6秒（~10倍——共有デーモンは並列化できるが per-VM はできない） |
| セッション初回の build | ウォーム | +~6秒（builder VM のコールドスタート） |
| bind mount の小ファイル I/O | 遅い | わずかに**さらに遅い**（同じホスト↔VM 共有モデル） |

短命コンテナのギャップはアーキテクチャ由来（1コンテナ1軽量 VM）なので、パッチ1つで縮む種類のものではありません。短命コンテナを回し続ける使い方（テストスイート・CI 的ループ）には、今日の時点で明確な税金です。

つまりピースは全部揃っているが、パフォーマンスの輪郭はこう言っています——**日常の多サービス開発スタックには、まだ早い**。（[計測方法と広い比較はこちら](https://github.com/suruseas/opossum/blob/main/docs/vs-docker-desktop.md)）

## 結論：いつ選ぶか（そして、まだ選ばない方がいいとき）

| あなたの状況 | 今日の選択 |
|---|---|
| 日常の多サービス開発スタック（app + DB + cache …） | **Docker / Colima / OrbStack** — 効く場面で速い（メモリはこの規模ではほぼ互角） |
| 短命コンテナを大量に回す（テスト） | **Docker** — per-VM の起動コストがここで痛い |
| `docker.sock`・`NET_ADMIN`・ホストカーネルが必要 | **Docker** — Apple container は設計上できない |
| 小さなイメージを1つ2つ、たまに、多くはアイドル | **Apple container** — 本当に軽く、分離もきれいで、ライセンス不要 |
| コンテナごとの VM 分離を、常駐 VM なしで欲しい | **Apple container** — ここだけは他のどれも提供しない |

これが 2026 年前半時点の正直な読みです：**1.0 のピースは揃い、互換性も本物。しかしパフォーマンスの輪郭ゆえに、普通の開発環境としては Docker や既存の代替の方が実用的です。** Apple container の現在のスイートスポットは狭く——小さく・たまに・大半はアイドル、なワークロード——ですが、その構造的な強み（常駐 VM なしのコンテナごと VM 分離）はランタイムの成熟とともに注視する価値があります。数リリース後にまた測り直しますが、メインスタックを今日移すことはしません。

## 自分の compose で試す（Docker と並走して安全）

自分の compose がどこに着地するか見たいなら、3コマンドで済み、Docker 環境には触れません（Apple container はイメージ・コンテナ・volume のストアが Docker と完全に別で、`opossum down -v` も自分のものしか消しません）：

```sh
brew install suruseas/opossum/opossum
cd your-project        # 既存の docker-compose.yml があるディレクトリ
opossum config         # 任意: 無視されるフィールドがあるか事前に確認
opossum up
```

リポジトリ：<https://github.com/suruseas/opossum>

この調査は続けて広げたいです。実際の compose ファイルで `opossum up` して何かが壊れたら、ぜひ教えてください（コメントでも [Issue](https://github.com/suruseas/opossum/issues) でも）。そして、このパフォーマンス数値をご自身のハードで測り直して違う結果が出たら、それも知りたいです。目的は売り込みではなく**正直な現在地**です。
