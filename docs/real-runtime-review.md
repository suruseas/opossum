# 実 `container` での検証手順

opossum は二層で検証する:

| 層 | 手段 | 確かめること | 頻度 |
|---|---|---|---|
| ロジック | fake シム | パース・順序・引数組み立て・失敗系 | 毎回 |
| 実機 | 実 `container`（macOS 26 の実機） | 現実環境で本当に動くか | 要所 |

「fake で論理的に正しい → 実ランタイムで現実でも動く」の二段で前進を確かめる。
この文書は後者（実機での検証）の**再現可能な手順**をまとめる。環境の状態に依存して
結果がブレた（builder が起動する/しない 等）ことがあり、そこから明文化した。

## いま検証されている `container` の版

**2026-09-09 / `container CLI version 1.4.1`（Homebrew formula `1.4.1`）/ macOS 26。**
（1.3.1 → 1.4.1 の差分と、1.4.1 で引き直していない節は `testdata/real-cli-output.md` の冒頭に索引がある。
1.4.1 固有の非互換は見つかっていない——`system status` の行は増えたが opossum が読む `status running` は残った。）
更新は `brew upgrade container`（Apple の pkg ではない）。更新後は `container system start`。

上の表の「日々」が意味を持つのは、fake が**現実の版と一致している**あいだだけ。その根拠は
[`testdata/real-cli-output.md`](../testdata/real-cli-output.md) 1枚にかかっている。かつて
そこが 1.0.0 採取のまま2節だけ 1.1.0 で更新され、**版が混在**していた——混在したゴールデンは
差分が出たときに「どの版で変わったのか」を答えられない。

**版を上げるときの手順**:

1. 上げる**前**に、いまの版で採れる観測を採る（更新すると二度と採れない）。生出力を
   分類前のバイト列のまま保存する
2. `testdata/real-cli-output.md` の**全節**を新しい版で採り直す（一部だけ更新しない）
3. 実測を前提にしているコードコメント・文書の版記述を、**測ってから**更新する。測り直して
   いない数値（ベンチ等）は版だけ書き換えない——偽になる
4. **案内の引き金を、実際に引き直す。** `testdata/real-cli-output.md` の「診断が一致させて
   いる文言と、その引き方」の表に、**行ごとにどこまで確かめたかが**書いてある。一致しなくなっても**出るのは「案内が無い」状態**で、テストは古い文言を入力にして
   いるので緑のまま通る——**気づく機会がここしかない**。

   結果は表の2つの列に、**`raw:<ファイル>` / `path-tried:<ファイル>` / `unverified` の
   どれか1つ**で始めて書く（そのあとに括弧で**範囲を狭める**但し書きを足してよい。広げる説明は
   書かない）。生出力は `testdata/error-wordings/` に置く。

   **指す生出力が無ければ `raw:` は書けず、その捕獲物に文言が入っていなければ、やはり書けない**
   ——`internal/runtime/wordingcite_test.go` が両方を確かめる。散文で留保するのをやめて語彙を
   減らしたのは、**丁寧に書いても、セルは「確認済み」に見えたままだった**から。

   **引けなかったことを書かずに「全節を採り直した」と書くと、確かめていない範囲まで確かめた
   ことになる**（実際に起きた）

## 日々（fake シム）

実ランタイム無しで、発行される `container` コマンド列を高速・無人で検証する。

```sh
make test            # 回帰ゲート（CI と同じ -race -cover で走る）

# end-to-end スモーク（発行コマンドは $FAKE_LOG に記録される）
FAKE_LOG=/tmp/opossum-fake.log \
OPOSSUM_CONTAINER_BIN="$PWD/testdata/fake-container.sh" \
  go run ./cmd/opossum -f examples/compose.yaml up
cat /tmp/opossum-fake.log   # 実際に叩かれた container 呼び出し
```

fake シムの出力は実 CLI に合わせてある（`testdata/real-cli-output.md` が golden、**いま 1.2.2**）。
版は上の「いま検証されている `container` の版」の節が正で、ここには書かない——2箇所に書くと片方が
古くなる。CLI を更新したら golden を採り直して同期すること（過去にそうなった経緯がある）。

## 実機で始める前のチェックリスト

```sh
container --version                 # 「いま検証されている版」の節と一致すること
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

**実機だけが答えを持つ主張の conformance 検査**（env-gated。実機レビューのたびに1回回す）:

```sh
make real-conformance
```

旗なしでは skip（日々のゲートは fake の領分）。**旗が立っているのに前提が欠けていると
Fatal**——「測ったつもりで測っていない緑」を検査自身が拒む。前提は runtime のもの
（`container` 不在・未起動・image が引けない）だけでなく、daemon の測定については
daemon のものも入る：文書上の名前が解決しないマシン、解決しても誰も答えないマシンでは
**赤になる**（Docker を入れていないマシンでこの target が赤になるのはこのため）。
現在の内容: socket 直 bind が通信まで通ること／symlink→socket が errno 95 で拒まれること
（下の 2026-08-27 の記録を参照）／`/var/run/docker.sock` の解決先を bind して **Docker daemon が
答えること**（`TestARealDockerDaemonAnswersThroughABoundSocket`、下の 2026-08-28 の記録）。測っているのは **socket ファイルそのものの bind** で、
listener は `net.Listen("unix", …)` の汎用 socket。

`OPSM-109` は3つのことを言う——拒否（errno 95）・境界（socket を自分のパスで、あるいは
symlink をファイルやディレクトリへ向けてマウントするのは通る）・逃げ道（リンク先を直接
bind すれば動く）。この検査が覆うのは、拒否（`TestARealSymlinkToASocketIsStillRefused`）と、
「socket を自分のパスで」（`TestARealSocketBindCarriesTraffic`）——後者は逃げ道が指す形でも
あるので、逃げ道もここで覆われている。境界の残りは：symlink→ディレクトリが下の
2026-08-21 の記録にあり、**symlink→ファイルはこの文書に記録が無い**（container 1.1.0 の
測定が `internal/orchestrator/orchestrator_test.go` のコメントに残っているだけ）。

`OPSM-106` が当たるパス——`isHostDevicePath` の前置一致（`/dev/`・`/tmp/.X11-unix`・
`/run/user/`・`/run/pulse`・`/var/run/dbus`）——で同じことが起きるかは**測っていない**。
前置一致なので、ディレクトリ（`/tmp/.X11-unix`）も socket ファイル
（`/run/user/<uid>/pulse/native`）も当たる。後者は測ったものと同じ形に見えるが、
試してはいない。

**build / health / completed の経路**は `compose.yaml` で確認（`container builder start` が要る）:

```sh
container builder start
go run ../cmd/opossum -f compose.yaml up     # db/cache healthy 待ち → migrate 完走 → web build/起動
go run ../cmd/opossum -f compose.yaml down
```

確認観点の例:
- `up`: network 作成 → 依存順に起動。`service_healthy` 依存は「Waiting for ... to be healthy」の後に
  起動、`service_completed_successfully` の migrate は前景で完走してから web が起動。
  途中で失敗したら起動済みコンテナと作成した network をロールバックし残骸を残さない。
- `ps`: 実 `inspect` の `status.state` を STATUS に、`publishedPorts` を PORTS に表示。公開ポートの
  `0.0.0.0` を IP と誤認しない（#M3）。
- `logs` / `stop` / `restart` / `up <svc>`: 発行される `container logs|stop|start` と対象が正しいか。
- 複数プロジェクト: `-p a` と `-p b` で同時起動でき、同じ service 名でも衝突しない。
- `down`: 逆順 teardown、`<project>-net` 削除。既に無い network への再 `down` は警告を出さない。

## 実機検証の記録

この節に書いた実機テストの名前は、ソースにその関数が実在することが検査される。消したテストを記録に残すときは打ち消し線で書く——~~`TestARealExample`~~ のように。打ち消した名前は「もう無い」として検査され（ソースに残っていれば赤）、実在するテストを名指した数には入らない。

- **2026-09-09 — `container` 1.4.1 への追従（main `9633a64`）**。`brew upgrade container`（1.3.1 → 1.4.1、1.4.0 は
  tag 破棄）のあと apiserver は止まっていて（`container system status` が exit 1・`apiserver is not running and not
  registered with launchd`）、`container system start` で上がった。golden をパーサの読むコマンドについて採り直し
  （`~/opossum-dogfood/results/v141-recapture/`、78 ファイル、`testdata/real-cli-output.md` の冒頭に 1.3.1 → 1.4.1 の
  索引）、**opossum が読む形で変わったものは無い**——`system status` は行が増えたが `status running` は残り、
  `inspect`／`ls -a --format json`／`network inspect` の key は消えていない、JSON の `\/` が `/` になった、
  文言（not found・no such container・volume in use・exit 64）は同じ。examples 4 project を 9/8 と同じ harness で
  `config → up → ps → logs --tail 5 → down -v --remove-orphans`：**すべて exit 0（mcp-stack の `logs` だけ、profile
  外の service を名指すので exit 1——9/8 と同じ）、各 `down` の後の残骸 0**。`doctor` は 8 項目とも通る
  （network も——9/4 の 1.3.1 更新直後に出た「containers can't reach the internet」は今回は出なかった）。
  生出力は `~/opossum-dogfood/sprint-0909/`。引き直していない節（`--platform`・port-attempt・OPSM の再現・build の
  失敗系・DNS spike）は golden の索引に名指ししてある。

- **2026-09-08 — compose の読みを docker と揃えた 20 本あまりのあと、examples を実機で通した（container 1.3.1・main `7a619c6`）**。
  `examples/hello.yaml`（alpine ×2）、`examples/compose.yaml`（redis:7・postgres:16・one-off の migrate・`./web` を
  builder で build）、`examples/app-stack`（postgres:16・redis:7・adminer:4・worker）、`examples/mcp-stack`
  （terraform-http。stdio profile の 2 つは `up` の対象外）の 4 project を `config → up → ps → logs --tail 5 →
  down -v --remove-orphans` の順に走らせ、**すべて exit 0、各 `down` の後の container・network の残骸 0**。
  healthcheck の待ち、one-off の完走（`migrate` は `ps` に stopped で残る）、build、port 公開
  （`0.0.0.0:8080->8080/tcp`）が実機で動く。生出力は `~/opossum-dogfood/sprint-0908/`（段ごとの `.out` と
  `summary.txt`）。

  同じ回で 2 つ観測した。build context を写し忘れた状態で `up` を打つと、`container build` が
  `context dir does not exist` で落ち、**先に起動していた cache・db・migrate は巻き戻されて
  「nothing this `up` started is left running」**——途中で失敗した `up` の巻き戻しの実機確認 2 度目。
  profile で起動対象外の service を `logs` に名指すと exit 1 で `confirm the service is up with
  `opossum ps``——動作としては妥当で、記録のみ。

- **2026-08-28 — 「daemon に届いた」を、走るもので測り直した（container 1.2.2）**。
  公開文書の「a Docker daemon was reached that way from inside a container」は、2026-08-27 の
  手書きの記録だけを根拠にしていた。既存の conformance が測っていたのは自前の listener との
  ping/pong ——**汎用の unix socket が通ること**で、daemon については何も言っていない。

  `TestARealDockerDaemonAnswersThroughABoundSocket` として固定した。`/var/run/docker.sock` を
  `filepath.EvalSymlinks` で解決し（この機械では `~/.docker/run/docker.sock`）、その解決先を
  コンテナに bind して中から `GET /_ping` を投げる。

  <!-- この段落は判定だけを書く。バッククォートで囲んだ語が判定として読まれ、ソース側の宣言と一致していることが検査される。観測した値や送るものは、空行を置いて次の段落へ。 -->

  判定は `HTTP/1.0 200 OK` と **`Api-Version:` ヘッダの両方**で、daemon を名指すのは後者。
  そのパスに普通の HTTP サーバを置いても 200 は返る——それは「何かが答えた」であって、
  文書が言っている「*daemon* に届いた」より弱い。**200 だけでは足りない**、が正しく、
  200 を見ていない、ではない。

  ホストで先に生バイト列を測って、`HTTP/1.0 200 OK` に続く `Api-Version: 1.55` を確認してある。

  **変異2件で、赤にできることを確かめた**（どちらも実機で実行）:

  | 当てた変異 | 結果 |
  |---|---|
  | 文書上の名前を、200 は返すが Engine API ではない socket に向ける | **赤**（`reply: b'HTTP/1.0 200 OK…'` が出たうえで `Api-Version:` の判定が落ちた＝コンテナは届いていて、判定だけが区別した） |
  | その socket の listener を落とし、ファイルだけ残す | **赤**（bind の前に「解決はするが誰も答えない」で Fatal） |

  **この検査は 2026-08-28 に実機で緑になった**（`container` 1.2.2、macOS 26。同じ run で
  既存の2本も緑）。`AGENTS.md` と `docs/compatibility.md` の日付を 2026-08-27 から
  2026-08-28 へ動かした根拠はこの run で、上の変異はその同じ木に当てたもの。

  前提の扱いはこのファイルの他の検査に揃えた——旗が立っているのに daemon が居なければ
  **skip ではなく Fatal**。文書自身が「入れただけでは何も答えない」と書いているので、
  測る側が同じ基準を自分に課さないと筋が通らない。

- **2026-08-27 — socket bind の「通る」と「使える」を分けて測った（container 1.2.2）**。
  `OPSM-109` の助言（リンク先の socket を直接 bind する）は、これまで「マウントが通る」まで
  しか確かめられていなかった。ホスト側（`$HOME` 配下）で unix socket を listen し、
  `python:3.12-alpine` のコンテナ内から接続して1往復（ping→pong）を実測:
  - **直 bind は通信まで通る**。`opossum up`（bind 指定の compose）でも `container run -v` 単体でも
    同じ。ホスト側 listener が `ping` を受信し、コンテナ側が `reply: b'pong'` を出力。
    → `OPSM-106` の「a host device or session socket is mounted → the mount exists with
    nothing behind it」は **socket 一般には偽**（少なくとも `$HOME` 配下の unix socket は
    実体つきで届く）。範囲はその後の修正で狭め直した——note は device node と session socket を
    分けて書き、session socket について何か通るかは測っていない、と言うようになった。
  - **symlink→socket の bind は 1.2.2 でも `errno 95` で拒否**（1.1.0 で採った記録と同じ形）。
    `OPSM-109` の診断（拒否の説明）は現役。
  - **docker.sock も、解決先を bind すれば本物の dockerd に届く**（同日追試）。
    `/var/run/docker.sock` の symlink は、この機械では Docker Desktop が置いていた——その解決先
    （`~/.docker/run/docker.sock`）をコンテナに bind し、中から HTTP `GET /version` で
    **200 OK（`Server: Docker/29.7.2`、Docker Desktop 4.88.0）を実測**。当時の案内
    （`OPSM-109` の docker 分岐が言っていた「compose の変更では直らない」）は、この形の
    機械では偽だった。その分岐はその後削除され、拒否は socket の持ち主を見なくなっている。
    ただし届く先はホストの Docker Desktop のエンジンであって、opossum が起動した世界ではない
    ——文言はこの但し書きごと直した。
  - 最初の2点（直 bind の疎通／symlink の拒否）は env-gated の conformance 検査（`internal/orchestrator` の
    `TestARealSocketBindCarriesTraffic` / `TestARealSymlinkToASocketIsStillRefused`）として固定。
    3点目（daemon に届くこと）はこの日は手で測っただけで、走るものが無かった——2026-08-28 に
    `TestARealDockerDaemonAnswersThroughABoundSocket` として固定した（上の記録）。

- **2026-08-21 — container 1.2.2 での初レビュー（補間クラスタ ＋ v0.20.0 の3本）**。
  実機で確かめたのは、fake が原理的に答えられない層——**コンテナに実際に届いた環境変数**。

  **1. 空に展開された参照が、ホストの値を引き継がないこと**（`environment:` の null は
  「ホストから継承」の意味なので、これは fake の外側）。同名の変数をホストから輸出した
  状態で起動し、コンテナ内の `env` を読んだ:

  | 書き方 | 届いた値 |
  |---|---|
  | `FROM_UNSET: ${NOPE}` | 空（ホストの値は届かない） |
  | `BRACELESS: $NOPE` | 空 |
  | キーの次の行に参照 | 空 |
  | `SPACED: ${NOPE} ${NOPE}` | 空白1つ |
  | `HAND_WRITTEN_NULL:` | **ホストの値**（意図的な継承は残る） |
  | `HAND_WRITTEN_EMPTY: ""` | 空 |

  最後の2行が対になっているのが要点。空にする修正が、**書いた人が本当に継承を求めた形**
  まで巻き込んでいないことを、同じ実行の中で示している。

  **2. フロー列の要素が消えて引数が減らないこと**。`command: [sh, -c, …, sh, first,
  ${NOPE}, last]` を起動し、コンテナ内で `$#` と各引数を出力: **`argc=3` で2番目が空**。
  要素ごと消えていれば `argc=2` になり、引数が黙って1つ減る。

  **3. `[OPSM-109]` の境界**（symlink が socket を指すときだけ拒否）。3通りを実機で:
  symlink→socket は `[OPSM-109]` で拒否し**解決後のパスを名指し**、socket を自分のパスで
  マウントするのは成功、symlink→ディレクトリも成功。1.2.2 でも境界は同じ。

  **4. env ファイルの展開と優先順位**。`env_file` 内の連鎖（`B=${A}` → `1`、
  `C=${A}-${B}-tail` → `1-1-tail`、`D=${MISSING:-fallback}` → `fallback`）と、
  プロジェクト `.env` からの値、シェルがプロジェクト `.env` に勝つことを、コンテナ内の
  `env` で確認。

  **5. 診断コードの付与**。`[OPSM-109]`（error）と `[OPSM-407]`（warning）の双方が
  コード付きで出ることを、上の実行の中で確認。

  いずれも `down` 後にコンテナ・ネットワークの残骸ゼロ。**新たな穴は出ていない。**

- **2026-08-21（続き）— env ファイルの読み取り（container 1.2.2）**。fake は「何がエラーになるか」
  までは見られるが、**通った値がコンテナに届くか**と**断ったときに何も起動していないか**は見られない。
  その2点を実機で確認した。

  **1. Windows のエディタが書く形が、そのまま通ること**。先頭 BOM ＋ CRLF ＋ 連鎖参照の
  `env_file:` を渡し、コンテナ内の `env` を読んだ:

  ```
  svc.env: <BOM>DB_HOST=db.example\r\n  DB_PORT=5432\r\n  DB_URL=${DB_HOST}:${DB_PORT}\r\n
  → DB_HOST=[db.example]  DB_PORT=[5432]  DB_URL=[db.example:5432]
  ```

  BOM も `\r` も値に混じらず、連鎖参照も解決している。

  **2. 断ったときに何も起動していないこと**。名前の中の BOM と、`KEY=VALUE` でない行の
  2通りで、いずれもコンテナが1つも作られないまま止まる。ネットワークも作られない。

  **3. 断りの文面が、実機の出力でも中身を出さないこと**。`.env` に置いたトークンらしき文字列は
  出力に現れず、ファイルと行番号だけが出る。ログや issue に貼られる経路を考えると、
  ここは端末の実出力で確かめる価値がある。

  いずれも残骸ゼロ。**新たな穴は出ていない。**

## ドッグフーディング価値検証（記録）

Homebrew 公開ゲート「十分な価値検証」の証跡。代表的な実 compose を実機（container 1.0.0 / macOS 26）で
end-to-end に回し、動作と穴を実測した記録を残す。

- **2026-07-03 — 単一フルスタック（web+db+cache+worker）**: version/networks/named volume/env_file/
  healthcheck/depends_on condition/container_name/restart を網羅した代表 compose を実機で検証。動いた:
  探索 as-is 起動・config 解決/検証・health-gating・discovery（worker が db/cache を bare 名解決）・
  published port が host から到達・rollback・down クリーン。**最大の発見**: DB+named volume が
  実 container で失敗（→「既知の落とし穴」）。派生バックログを起票・対応。
- **2026-07-04 — 広域 multi-project ＋ build-from-source（最優先）**: 同一の service 名
  （db/web/builder）を持つ2プロジェクト `shopapi`/`blog` を**並行起動**し、以下を実機で実証:
  - **discovery がプロジェクト内に閉じる（discovery 面）**: `blog` の web が bare `db` を
    `db.blog.opossum`（192.168.67.3＝自分の db）に解決し、`shopapi` の db（192.168.66.3）には漏れない。
    ネットワークも `shopapi`=192.168.66.x / `blog`=192.168.67.x で分離。
  - **build-from-source（`build:`）**: 各プロジェクトが `shopapi-builder:latest` / `blog-builder:latest`
    を Dockerfile から個別にビルドして起動（ログに `built-from-source-...`）。
  - **named volume 分離**: 同名 volume `shared` が `shopapi_shared`/`blog_shared` に分離自動作成、
    `down -v` は呼び出し側のみ削除し他プロジェクトのデータを残す（footgun 修正の実機確認）。
  - いずれも down で残骸ゼロ。**新たな穴は出ず**、複数プロジェクト分離が名前＋network＋volume＋
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

## 実在 docker-compose.yml 互換性検証

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
  **結果: 実在 compose 頻出の未対応記法を3件発見**（as-is では動かず → いずれも issue 化して後で対応）。
  - **[高] long-form volume 構文でパース失敗**: `- {type: bind, source, target, read_only}` を
    `cannot unmarshal !!map into string` で拒否（proxy の nginx.conf マウント）。→ その compose は起動不能。
  - **[高] `build.target` が黙って無視される**: `Build` に Target が無く、Service と違い未知キーを
    Unsupported に記録しないため警告も出ず、マルチステージの意図したステージでビルドされない。
    awesome-compose 39 ファイル中 **16** が使用。
  - **[中] `secrets` 未対応**: `POSTGRES_PASSWORD_FILE=/run/secrets/db-password` 等の `_FILE` パターンが
    secret 未マウントで機能しない（39 中 8 が secrets 使用）。
  - **数量的把握**（awesome-compose 39 ファイル）: build.target=16, secrets=8, long-form volume=3, configs/env_file=0,
    build なし（pre-built のみ）=12。→ **as-is 互換の主障壁は build.target と long-form volume**。優先実装対象。
- **rung 2 再検証（2026-07-04, long-form volume と build.target の実装後）**:
  - **[解消確認]** `nginx-golang-postgres` を再度 `config` すると long-form volume が
    `./proxy/nginx.conf:/etc/nginx/conf.d/default.conf:ro` に正規化され、`build.target: builder` も表示され、
    **完全描画**（以前は long-form volume で parse 不能だった）。build 時に `container build --target builder` が
    実機に届き builder ステージがビルドされることも確認。→ 実在 compose の**主障壁だった 2 件は解消**。
  - **[訂正済み — build 系も home 配下で完動]** 当初 build 時の `COPY` が
    `failed to calculate checksum ... "/<file>": not found` で失敗し「builder が COPY 不能」と誤結論したが、
    チーム独立検証＋精密再現で**誤りと判明**。原因は検証を **scratchpad（`/private/tmp/...`）** で行っていたこと。
    最小再現（2行 Dockerfile・直 `container build`）で場所だけ変えると: **home（`/Users/...`）成功／`/tmp` 成功／
    `/private/tmp`（real temp）失敗**。Apple builder VM は home と `/tmp` を mount するが `/private/tmp` を mount
    しないため。**realpath 強制解決は逆効果**（`/tmp/x`→`/private/tmp/x` で成功→失敗）。
  - **[build 系 end-to-end 実証]** `docker/awesome-compose` の `nginx-golang`（**build.target ＋ long-form volume**
    使用）を `$HOME` 配下にコピーして `opossum up`: backend の **go build 成功**、long-form volume 正規化・
    `build.target: builder` 適用、backend/proxy 起動、`curl localhost:80` が nginx→go backend 経由で応答、
    `down` で残骸ゼロ。→ **実ユーザーの home 配下プロジェクトは build 系も完動**。「build 系 full run 不可」の
    公開懸念は解消。builder 非対応の場所/symlink への配慮は別途 enhancement として検討。
- **rung 3（2026-07-04）— 実 Rails 7 repo（`ryanwi/rails7-on-docker` の `compose.yaml` 無改変）**:
  `web`(build: custom `development.Dockerfile`)＋`db`(postgres:17)＋`redis` の3層。`$HOME` 配下で検証。
  - **parse 完全**: custom dockerfile 指定・`command` の `bash -c` shell-split・**bind＋named volume 混在**
    （`.:/usr/src/app` ＋ `bundle:/usr/local/bundle`）・`env_file`・**depends_on の条件混在**（db=healthy /
    redis=started）・healthcheck を正しく解決。
  - **env_file 欠如の挙動**: repo は `.env` を gitignore（`.env.example` も無し）。opossum は `env_file ".env" not
    found` で**明確にエラー**（docker compose 準拠の正しい挙動）。`.env` を用意すれば進行。→ 長形式
    `env_file: {required: false}` 未対応（低）。
  - **build 駆動 正常**: `opossum build web` が `ruby:3.3.9-slim` ベース＋`-f development.Dockerfile` で build を
    駆動、context 転送・apt/bundle 進行を確認（opossum の責務は完遂。bundle install 完了は待たず停止）。
  - **postgres named-volume 制約に的中**: `db` の `pg_data:/var/lib/postgresql/data`（PGDATA 回避なし）で
    `initdb: error: directory ... exists but is not empty ... lost+found ... Create a subdirectory` ＝db が
    healthy にならず、full `up` は明確なエラーで rollback。**opossum のバグではなく既知の実制約**
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
    - → **Vite+Vue パターンは opossum で完全に動く**。rung4 の失敗は *repo の Dockerfile の陳腐化* が原因で、
      opossum/Vite/Vue 側の問題ではないことを確定。

### 総括（実在 compose 互換性）
- **opossum は多様な実 docker-compose.yml を正しく parse し、home 配下から build を駆動する**（simple→Rails→Vite で確認）。
- **full run の可否は compose/repo 側に依存**: pre-built（wordpress-mysql）と clean-build（nginx-golang）は完動。
  DB-on-named-volume（Rails の postgres）は PGDATA 回避が要る。一部 repo は Dockerfile 自体が壊れている
  （Vite の pnpm/node 不整合）。
- 実装で潰した実在 compose の主障壁: **long-form volume / build.target**。残る既知: postgres の named volume・
  secrets・builder 非対応の場所・env_file required:false。

## 実在 compose 広域検証（pre-built スタックの多様性）

前節の続き（ユーザ要望）。種類を広げて他リポジトリの pre-built docker-compose.yml を `$HOME` 配下で実機検証。

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
    `accepting connections`。postgres は volume 未使用のため PGDATA 問題も出ない。残骸ゼロ。
- **知見**: pre-built 実 compose は opossum で概ね完動（config bind mount・named volume 名前空間化・`.env` 補間・
  published port・exec すべて実機で確認）。動かない場合は **repo 側の陳腐化**（unpinned image と古い config の不整合）
  が主因で、opossum のバグではない。

### 第2弾（realistic app ＋ runtime 機能ギャップ, 2026-07-06）

- **gitea-postgres（`docker/awesome-compose`, 自己ホスト git, 無改変）**: gitea＋postgres。**postgres の named-volume 制約（postgres
  named-volume）に的中する現実例**。db が `db_data:/var/lib/postgresql/data` を使い initdb が mount point で失敗→
  **db stopped**（opossum は stopped を正報告）。gitea 自体は起動し `localhost:3000` → HTTP 200 で web 応答するが、
  db 無しでは非機能。→ **gitea/nextcloud 等の自己ホスト app compose は postgres データを named volume に直載せする
  ことが多くこの制約に当たる**。回避は PGDATA サブディレクトリ（→ opossum 側の警告 ergonomics を検討: 別 issue）。
- **wireguard（`docker/awesome-compose`, VPN, 無改変）**: **Apple container の runtime 機能ギャップの例**。
  - opossum が **`cap_add` / `sysctls` / `container_name` / `restart` を無視警告**（surfaced、silent でない）。
  - up は `/usr/share/appdata/wireguard/config` 等 **Linux ホスト前提の bind mount が存在せず**失敗（明確にエラー
    伝播・stopped 正報告）。加えて `NET_ADMIN`/`SYS_MODULE` cap やカーネルモジュール（`/lib/modules`）は Apple
    container で提供されない。→ **カーネル機能/特権 cap/Linux ホストパスに依存する compose は Mac（Apple container）
    では動かない**（Docker Desktop on Mac でも同種の制約）。opossum は未対応フィールドを警告し失敗を正しく伝える。
- **portainer（inspection）**: `command: -H unix:///var/run/docker.sock` ＋ `/var/run/docker.sock` の bind mount＝
  **Docker socket 前提**で、当時の結論は「Apple container にはアーキ的に非適用（docker socket が無い）」。

  2026-08-27 に分かったこと（上の記録）。*これらの*コンテナについて答えるものが無いのは
  変わらないが、理由は socket の不在ではなく XPC で話すこと。そしてこの compose は
  **そもそもマウントで止まる**——`/var/run/docker.sock` が socket への symlink である機械
  （Docker Desktop の入った Mac がそう）では `OPSM-109` が起動前に拒否し、`OPSM-204` の
  警告までたどり着かない。リンク先を直接 bind すればホストの daemon には届くが、届く先は
  別の集合だ、というのが `OPSM-204` の言い分。
- **知見（第2弾）**: opossum は「動かせないもの」も**誠実に扱う** — 未対応 runtime フィールド（cap_add/sysctls/
  devices/privileged）は警告し、存在しない host パスの bind は明確にエラー伝播。動かない主因は (a) postgres の named-volume 制約の
  postgres named-volume（頻出・要 PGDATA 回避）、(b) Linux カーネル/特権/docker-socket 依存（Apple container の
  範囲外）で、いずれも opossum のバグではない。docker-socket の部分は 2026-08-27 に狭まった
  ——上の portainer の項を参照。
- **nextcloud-postgres（`docker/awesome-compose`, クラウドストレージ, 無改変）**: nextcloud＋postgres。gitea 同様
  db が `db_data:/var/lib/postgresql/data` を使い **named-volume 制約に的中→db stopped**。nextcloud web は `localhost:80` → 200。
  **datadir 警告を実 app で実証**: up 時に `warning: service "db": a named volume mounted at
  /var/lib/postgresql/data will fail Postgres initdb ... PGDATA=.../pgdata` が発火（実 app で回避を誘導できる
  ことを確認）。→ この警告の ergonomics が gitea/nextcloud 系の頻出パターンで有効。

## 既知の落とし穴

- **Postgres の data ディレクトリを named volume に直接載せると起動失敗する**（ドッグフーディング検証で確認）。
  named volume は `lost+found` を含む mount point としてマウントされ、**postgres の `initdb`** が
  「非空のデータディレクトリ」を拒否する（`directory ... exists but is not empty ... using a mount point
  directly ... not recommended`）。回避: **mount のサブディレクトリ**を使う
  （`PGDATA=/var/lib/postgresql/data/pgdata`）。この場合 `up` は health-gate で never-healthy を検知し
  `container is not running ... check opossum logs <svc>` を返して rollback する。
  **※ MariaDB/MySQL はこの制約を受けない**（datadir が空でなくても既存 DB を検出して初期化を続けるため）。
  実在の `wordpress-mysql` compose（rung 1）が `db_data:/var/lib/mysql` を無改変で正常起動することを確認済み。
- **builder が `StreamClosed / HTTP2 ProtocolError` で起動しないことがある**。`build:` サービスの
  ビルドが `Timeout waiting for connection to builder` で失敗する。opossum は失敗を exit 1 で
  正しく伝播する。回避: builder 非依存の `hello.yaml` でレビューするか、builder を起動し直す。
- **restart で IP が再割当される**。`container start` が新しい IP を振るため。コンテナ名・設定は
  保たれ、名前解決（DNS 再登録）には影響しないが、IP を直接握っている外部クライアントは注意。
- **`gh pr create/edit` が org スコープ不足で GraphQL エラー**になる環境がある。PR の作成/編集は
  `gh api -X POST/PATCH repos/<owner>/<repo>/pulls`（本文は `-F body=@file`）で回避する。

## 途中で失敗した `up` の巻き戻し（2026-09-06 実機・container 1.3.1）
`cache` → `db`（`/mnt/…` を bind、macOS では作れない）で `opossum up`：cache が起動 → db の `OPSM-104` →
`Rolled back cache — stopped and removed; nothing this `up` started is left running` が出て、
`container ls -a` と `network ls` にプロジェクトのものは残らない。docker compose v5.5.0 は同じ形で
cache を動かしたまま exit 1（`ps -a`: cache running / db created）——巻き戻すのは opossum 側だけ。

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
**2026-09-06 追記（container 1.3.1・kernel 6.18）**：VM は cgroup v2 を controller 無しでマウントしており、NPE は同梱 JDK 17.0.1 の側。**7.16.3・7.17.0（JDK 17.0.1）は 1.3.1 でも同じ NPE、7.17.28（JDK 22.0.2）は `started` まで行き 9200 が応答**。「ES 7.x は不可」ではなく「古い JDK を同梱する patch は不可」。compatibility.md をそのとおりに直した（`~/opossum-dogfood/results/v131-claims/p16-es-cgroup.txt`）。
