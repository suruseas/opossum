# 実 `container` CLI 出力リファレンス（fake シム同期用）

opossum が出力を**パースする**コマンドについて、実 `container` / macOS 26 で採取した
stdout+stderr と exit code の golden。`testdata/fake-container.sh` はこれに合わせて出力を
返し、`internal/runtime/runtime_test.go` の忠実性 eval はこの文字列を各パーサに流して
整合を確認する。**CLI 更新時はここを再採取して同期すること。**

**最終検証: 2026-09-04 / `container CLI version 1.3.1`（Homebrew formula `1.3.1`）。**
下記の節を実機で採り直した（生出力は `~/opossum-dogfood/results/v131-recapture/`、68 ファイル、
各ファイルは `$ <コマンド>` → `--- exit code:` → `--- stdout ---` → `--- stderr ---` の形）。各節に
付いている古い採取日は、その記述が**最初に**確かめられた日。**1.3.1 で引き直していない主張**は
節の中で名指ししてある（「DNS 解決の挙動」の前半、`stats` の `--format`／ストリーミング既定）。

**1.2.2 → 1.3.1 で変わったもの（節ごとの注記が正、ここは索引）**：
- `network inspect` / `network ls --format json`：`status.ipv6Subnet` が増えた（キー増のみ）
- `inspect` / `ls -a --format json`：`publishedPorts[].count`・`status.networks[].mtu`・
  `mounts[].type.volume.{cache,format,sync}` が増えた（キー増のみ。一度も起動していない
  `buildkit` は `status.startedDate` を持たず、stopped の run コンテナは持っていた——**観測2件、規則は未確認**）。
  opossum のパーサが読むキーは1つも消えていない
- `volume delete`：引数なしは `USAGE:` ブロックでなく `Error: no volumes specified and --all not
  supplied`（exit 1）。成功時は volume 名を stdout にエコーする（1.2.2 は無出力）
- `stats`：**不在のコンテナ名を渡すと `Error: no such container: <name>`・exit 1 で呼び出し全体が
  失敗する**（1.2.2 は黙って飛ばしていた）。stopped は今も飛ばして exit 0。→ opossum は
  存在するコンテナだけを渡す（`createdContainers`）
- `run --platform` の失敗2文言は両方健在だが、引き金が入れ替わった（下の節）
- `container images ls`（複数形）は 1.3.1 で `Plugin 'container-images' not found.`・exit 64。単数の
  `image ls` を使う（`doctor` の案内文を直した）。**1.2.2 で通っていたかは測っていない**（比較対象が消えていた）
- 更新直後、コンテナ側のネットワークが全滅した（egress・コンテナ間・DNS。CLI 自身の pull は
  ホスト側なので通る）。`opossum doctor` が `network containers can't reach the internet` で
  検出し、案内どおり `container system stop && container system start` で直った

**その回で「全節・変化なし」と書いたのは誤りだった（2026-08-23 訂正）。**
`--platform` の節が載せている失敗の文言は、1.2.2 で**もう1つ増えていた**。採り直したのは
**実機で再現できた部分だけ**で、`--platform` の節のうち**失敗時の文言**は、再現に arm64
ビルドの無い image が要るという理由で**引き金を実際に引き直していなかった**（同じ節の
`--platform --rosetta` で起動するほうは採り直してある）——にもかかわらず、まとめは全節に
ついて書いていた。
再現できなかった節がどれかを、そのときここに書いていれば済んだ話（→ #421 / #477 / #478）。

以前は 1.0.0 採取のまま2節だけが 1.1.0 で更新されており、版が混在していた。混在した
ゴールデンは「差分が出たとき、どの版で変わったのか」を答えられない——fake が現実と
一致していることの根拠がここ1枚にかかっている以上、更新のたびに**全節**を採り直す。

## `container system dns list`  (exit 0)
```
DOMAIN
opossum
```
→ `DNSDomainExists(domain)`: 行を trim して一致判定。

## `container network create <name>`  （新規, exit 0）
```
<name>
```

## `container network create <name>`  （既存, exit 1）
```
Error: network <name> already exists
```
→ `EnsureNetwork`: 出力に `exist` を含めば「既存＝OK」として nil。

## `container network delete <name>`  （存在, exit 0）
```
<name>
```

## `container network delete <name>`  （不在, exit 1）
```
Error: failed to delete one or more networks: ["<name>"]
```
→ `DeleteNetwork`: この文字列は **`not found` を含まない**。`networkAlreadyGone` が
`failed to delete one or more networks` も「既に無い」と見なして誤警告を抑制する。

## `container network inspect <name>`  （存在, exit 0）
```
[
  {
    "configuration" : {
      "creationDate" : "2026-07-22T07:47:15Z",
      "labels" : { },
      "mode" : "nat",
      "name" : "<name>",
      ...
```
1.3.1: `status` に `ipv6Subnet` が増えた（`ipv4Gateway`/`ipv4Subnet` はそのまま）。抜粋部は一致。

## `container network inspect <name>`  （不在, exit 1）
```
Error: network not found: <name>
```
→ `NetworkExists`: 終了コードだけを見る。`destroy` はこれを削除の可否のゲートに
使うので（存在しない網を計画に並べない）、この契約が崩れると destroy は `down`
より消し残す。2026-07-30 に macOS 26 上で実測。

## `container inspect <name>`  （不在, exit 1）
```
Error: container not found: <name>
```
→ `InspectIP`: capture がエラーを返すため `""`（stopped 扱い）。

## `container inspect <name>`  （稼働中, exit 0）
`status.state` に状態（`running` / `stopped`）、`status.networks[].ipv4Address` に IF アドレス、
`configuration.publishedPorts[]` に公開ポート（`containerPort` / `hostAddress`（`0.0.0.0`）/
`hostPort` / `proto`）。詳細な JSON は `fake-container.sh` の inspect ケースを参照。
1.3.1 で増えたキー：`publishedPorts[].count`・`status.networks[].mtu`・`mounts[].type.volume.{cache,format,sync}`
（`encoding/json` は黙って捨てる。シムの形は 1.3.1 の真部分集合のまま）。
→ `Inspect(name)`: この1回のパースで State / IP / Ports / Labels / Exists を取り出し、
`ps` の IP・PORTS・STATUS 列と、IP/Label 系ヘルパの両方に使う。
ラベルは `configuration.labels`（マップ）。`run -l opossum.project=<name>` を付けると:
```
"configuration" : { "labels" : { "opossum.project" : "demo" }, ... }
```
→ `InspectLabel(name, key)`: これを読んでコンテナの所属プロジェクトを判定。`--dns-domain` 未設定
（bare 名）時の同名衝突ガードと所属メタデータに使う。

## DNS 解決の挙動（spike で確認 / 複数プロジェクト分離の根拠）
**1.3.1 で引き直したのは後半（登録済みドメイン＋サブドメイン名前空間化：proj1/proj2 の bare `db` が別 IP に解決）だけ。**
**前半の「未登録ドメインで NXDOMAIN」は 1.2.2 の観測のまま**（1.3.1 では引いていない）。
- **登録済みドメイン必須**: `--dns-search proj1`（未登録）だと相手を bare 名で引くと **NXDOMAIN**。
  登録済み `opossum` なら解決成功。`system dns create` は sudo・システム共有。
- **サブドメインで名前空間化できる**: `--name db.<proj>.opossum` ＋ `--dns-search <proj>.opossum`
  にすると、登録済みドメインが `opossum` 1つでも `db.<proj>.opossum` が登録・解決され、peer は
  bare `db` を**自プロジェクトの** `db.<proj>.opossum` に解決する（実機で proj1/proj2 が別 IP に
  解決することを確認）。→ opossum はこれで単一ドメインのまま複数プロジェクトを自動分離する。

## `container inspect <name>`  （終了済み, exit 0）— **終了コードは出ない**
プロセスが終了したコンテナは `status.state: "stopped"` になり、`status.networks` は空配列。
採取（`run --name X alpine:3.20 sh -c 'exit N'` 後に inspect）で確認した重要事実:
**exit 0 のコンテナと exit 3 のコンテナの inspect 出力は `state:"stopped"` で完全に一致し、
終了コードを表すフィールドは存在しない**。
```
"status" : { "networks" : [ ], "startedDate" : "...", "state" : "stopped" }
```
→ `depends_on: service_completed_successfully` の成否判定に inspect は使えない。
終了コードが観測できるのは **フォアグラウンド `container run` の戻り値のみ**（`run ... sh -c 'exit 3'`
の rc=3 を確認）。このため opossum は completed 対象サービスを `-d` なしで実行して exit 0 を待つ。
（この盲点は「JSON 形を推測せず実機出力を採取する」方針で先に潰した。）

## `container volume delete <name>`  （#59 `down -v` の根拠 / 2026-07-03 実機確認）
```
# 1.3.1（引数なし, exit 1）:
Error: no volumes specified and --all not supplied
# 1.2.2 までは USAGE: container volume delete [--all] [--debug] [<names> ...] のブロックだった
```
（`delete` は `rm` エイリアスあり。使用中の volume は `volume 'X' is currently in use and cannot be
accessed by another container, or deleted` ＋ `Error: delete failed for one or more volumes: [...]`・exit 1）
実機ラウンドトリップ（初出時は `container CLI version 1.0.0`）:
```
$ container volume create opossum-review-vol   # 作成
$ container volume ls                           # -> named / local として一覧
$ container volume delete opossum-review-vol    # 削除（exit 0。1.3.1 は volume 名を stdout にエコー。「無出力」は 1.0.0 期の記録で、1.2.2 は未測）
$ container volume ls                            # -> もう出ない（削除確認）
```
→ `runtime.DeleteVolume(name)` は `volume delete <name>` を発行（best-effort、使用中/不在は無言でスキップ）。
`Down(removeVolumes=true)`（`down -v`）が `namedVolumes()`（bind/匿名を `isHostPath` で除外）に対して発行する。

## `container stats [<names>...] [--no-stream]`  （#108 `opossum stats` の根拠 / 2026-07-06 実機採取）
`Container ID` / `Cpu %` / `Memory Usage`（`x MiB / y GiB`）/ `Net Rx/Tx` / `Block I/O` / `Pids` を表示。
既定はストリーミング（ライブ更新）、`--no-stream` で1スナップショットのみ、`--format json|table|yaml|toml`。
```
$ container stats --no-stream <name>
Container ID  Cpu %    Memory Usage         Net Rx/Tx            Block I/O            Pids
<name>        0.79%    29.41 MiB / 1.00 GiB 18.08 KiB / 0.57 KiB 25.68 MiB / 0.00 KiB 6
```
実機で確認した重要挙動: **複数コンテナ名を渡すと1テーブルにまとめて表示**し、**stopped のコンテナは
グレースフルにスキップ**（running 分のみ表示、exit 0）。**不在の名前は 1.3.1 では飛ばさない**——
`Error: no such container: <name>`・exit 1 で、同時に渡した running 分も表示されない（1.2.2 は
飛ばしていた。2026-09-04 実測、`stats-absent-only.txt` / `stats-multi-with-stopped.txt`）。
→ `opossum stats` は出力をパースせず passthrough（`runtime.Stats` が `stream` で stdio 直結）するため
出力形式には依存しないが、**渡す名前は存在するコンテナに絞る**（`Orchestrator.createdContainers`）。
`--format json|table|yaml|toml` とストリーミング既定は help の記述で、1.3.1 では `--no-stream` しか引いていない。

## `container image inspect` / `image delete`  （#126 `opossum images` / `down --rmi` の根拠 / 2026-07-06 実機採取）
`opossum images` の PRESENT 判定と `down --rmi` の削除に使う。実機で採取した exit セマンティクス:
```
$ container image inspect alpine:3.20        # 存在  -> exit 0
$ container image inspect nonexistent:none   # 不在  -> exit 1
$ container image delete --force nonexistent:none   # --force で不在を無視 -> exit 0
```
→ `ImageExists(ref)` は `image inspect <ref>` の exit code（0=present）。`DeleteImage(ref)` は
`image delete --force <ref>`（best-effort、不在/使用中は無言でスキップ、コンテナ削除後に実行）。
サブコマンド名は `image {inspect,delete}`（`delete` は `rm` エイリアスあり、`list` は `ls`）。

## `container run --platform <p> [--rosetta]`  （#130 compose `platform:` の根拠 / 2026-07-06 実機採取）
`container run --help` に **`--platform <platform>`**（マルチプラットフォーム image 用）、**`--rosetta`**（コンテナ内 x86-64 エミュ有効化）、`-a/--arch`（既定 arm64）が存在。amd64 専用 image は arm64 既定だと失敗するが、`--platform linux/amd64 --rosetta` で起動可能:
```
$ container run -d --platform linux/amd64 --rosetta redislabs/redismod   # amd64 専用 image
$ container exec <name> redis-cli ping   # -> PONG（Rosetta で稼働）
```
→ `runtime.Run` は `RunOptions.Platform` があれば `--platform <p>` を発行し、`p` に `amd64`/`x86_64` を含めば
`--rosetta` も付与。`orchestrator` は compose `platform:`（`Service.Platform`）を配線。

### 失敗時の文言（**1つではない** / 2026-08-23 に 1.2.2 で採取）

この節は長く `does not support required platforms` だけを載せていた。**1.2.2 はもう1つの言い方もする**ので、両方を書く。どちらが出るかは image によって決まり、選べない:

```
# 1.2.2: image index が arm64 を持たない（excalidraw/excalidraw-room:latest）——取得前に落ちる
$ container run --rm excalidraw/excalidraw-room:latest true
Error: platform linux/arm64
# 1.3.1: 同じ image は 12 blob を全部取得してから、下の「does not support」で落ちる（2026-09-04）。
# `Error: platform linux/arm64` のほうは、amd64 だけの image を既定 arm64 で走らせると出る
# （platform-image-no-arm64-131.txt）。2文言とも 1.3.1 に健在、引き金が入れ替わっただけ

# 取得してから不一致が分かる（Compose-Examples/examples/cs2-dedicated-server, atlas）
Error: image sha256:6822b9… does not support required platforms
```

**名指しされるのは「無いほうの platform」**。arm64 だけの image を amd64 として要求すると、そう言う:

```
$ container build --platform linux/arm64 -t x:test .   # arm64 だけの image を作る
$ container run --rm --platform linux/amd64 x:test true
Error: platform linux/amd64
```

だから `platform linux/` に広く一致させてはいけない——**この失敗に「その platform を要求しろ」と答えることになる**。`runErrorHint` は arm64 の形だけを、`Error: ` から始めて一致させる。`platform linux/arm64` だけだと `--platform linux/arm64` という**正当な指定**の部分文字列でもあるので、この失敗**以外**から来たテキストを拾わないため（`RunError.Stderr` に入るのは子プロセスの stderr だけで、コマンド行が混ざる経路は現状無い——「いま誰かが指させる経路」への備えではない）。

前の節が「1.2.2 で全節を採り直して変化なし」と書いているのに、この文言の変化は入っていなかった。**引き金を実際に引き直していなかった**ため（→ #421 への追記、#478）。

## 診断が一致させている文言と、その引き方  （2026-08-23 に 1.2.2 で確認、2026-09-04 に 1.3.1 で引き直し）

opossum は、ランタイムの出力の文言に一致させて案内を出す。**一致しなくなっても、出るのは
「案内が無い」状態**で、テストは古い文言を入力にしているので緑のまま——**壊れた形ではなく、
最初から無かった形**に見える（実際に起きた: #477）。

だから、それぞれ**どう引くか**をここに書く。**引けなかったものは、「引けない」ではなく
「どの経路を試して、どうなったか」を書く**——この表を書く過程で「引けない」と3回書いて、
**3回とも別の経路が見つかった**（うち**上流の文言まで届いたのは2件**で、残る1件は経路だけ）。
示せるのは**思いついた道で届かなかったこと**までで、それは
「引けない」とは違う。

**数え方**：単位は「**別々に引かないと確かめられないもの1つ**」＝表の1行。`OPSM-412` の2つの
文言は**別の image が要る**ので2行（片方だけ黙りうることは #477 が実証した）。`buildhint` の
3群は、1群につき1つの状況で引けると見込んで3行にしていた——**経路については確かめた**
（3群とも独立に発火する）。上流の文言のほうは未確認なので、そちらを引いたときに割り直す
かもしれない。数える対象で答えが変わるので、3つとも書いておく：**一致させている文字列は17個**、
**複数条件の AND を1つに畳むと14**（`OPSM-107` が1、`OPSM-103` が2、畳まれる）、
**`grep -o 'strings.Contains'` で数えるソース上の出現は10**（`buildhint` は8文言を1つの
ループで回す。`grep -c` は行を数えるので9——1行に2つある箇所があるため）。
行11・12 は正規表現と `strings.Fields` なのでこの数には入らない（行10 は `runtime.go` の
`strings.Contains` で、行9 と同じもの）。

**この表が数えているのは、ランタイム自身の出力の文言に一致させていて、外れると
「何も言わなくなる」もの**。2つの軸で切っている:

- **上流が image のもの**は別（#480）——`OPSM-101`（`initdb:` + `lost+found`）と
  `OPSM-105`（`chown` + `Operation not permitted`）と `OPSM-110`（`(unused mount/volume)` +
  `pg_upgrade`）は、コンテナのログに一致させている。この面の生出力もここに置いてある
  （`pg17-*.txt` 5件・`pg18-*.txt` 8件）が、**表には入れない**——上流が image である以上、
  版を書いた記録の形が違う（#480 で決める）
- **外れても見えるもの**は別——`runtime.go` の `"exist"`（network create の冪等化）は文言が
  ずれると**失敗する**。`"not found"`（teardown の冪等化）は失敗しないが、**余計な警告が出る**。
  どちらも見える。**黙るのは見えない**

**この2軸で外れる上流が他にもある**（射程外だが、同じ壊れ方をする）：`audit.go` の tinyproxy
ログ（`Request (file descriptor N):`——外れると egress の宛先一覧が黙って空になる／上流は
image）、`compose/load.go` の go-yaml の文言（**上流はライブラリ**という第3の上流。外れ方は
一様ではない——`yaml:` は素のエラーに退化するだけだが、`already defined at line` が外れると
**キーの二重定義に「値の形が違う」という別の助言**が出て、`cannot unmarshal` が外れると
**エコーされた値が消されずに出る**＝#450/#451 で直した情報漏れが戻る）、`hoststats.go` の `pgrep`/`lsof` の出力（外れると
HOST FOOTPRINT が黙って `—`／上流はホストのコマンド）。

行6〜12 は `[OPSM-nnn]` を持たないが（build の3群も `hint: …` だけでコードが無い）、
**外れると何も言わなくなる**ので入れている。
コードを索引にするのではなく、**黙り方**で切ること。**行11・12 は黙るより悪い**——
「問題なし」と言う。

**「未確認」は「引き方が無い」ではない**——引いていない、というだけ。**書けるのは
「どこまで確かめたか」だけ**で、「確かめようがない」は別に示さないと言えない
（上の3件がその実例）。

**列が2つある理由**：**経路**（この文言に当たれば案内が出る、という配線が生きているか）と
**上流の文言**（`container` などが**いまもその文言を出すか**）は別のこと。前者は文言を自分で
偽装すれば確かめられるが、それでは**上流が言い換えたことは永久に検出できない**。この表が
存在する理由は後者なので、混ぜない。

| # | 診断 | 一致させている場所 | 上流の文言（**全部**が捕獲物に入っていること） | **経路** | **上流の文言** |
|---|---|---|---|---|---|
| 1 | OPSM-412 | `orchestrator.go` `runErrorHint` | `does not support required platforms` | `raw:platform-does-not-support-131.txt` | `raw:platform-does-not-support-131.txt` |
| 2 | OPSM-412 | 同上 | `Error: platform linux/arm64` | `raw:platform-image-no-arm64-131.txt` | `raw:platform-image-no-arm64-131.txt` |
| 3 | OPSM-107 | 同上 | `failed to resolve` `in rootfs` | `raw:rootfs-resolve-131.txt` | `raw:rootfs-resolve-131.txt` |
| 4 | OPSM-201 | 同上 | `Address already in use` | `raw:port-in-use-duplicate-publish-131.txt` | `raw:port-in-use-duplicate-publish-131.txt` |
| 5 | OPSM-103 | `isStorageAttachmentError` | `VZErrorDomain` `Code=2` `storage device attachment is invalid` | `raw:vzerror-shared-named-volume-131.txt` | `raw:vzerror-shared-named-volume-131.txt` |
| 6 | build（cache 破損） | `buildhint.go` | `unable to read root manifest` | `raw:build-cache-path-only-131.txt` | `unverified` |
| 7 | build（resource） | 同上 | `rpc error: code = Unavailable` | `raw:build-resource-path-only-131.txt` | `unverified` |
| 8 | build（disk full） | 同上 | `No space left on device` | `raw:build-disk-full-131.txt` | `unverified` |
| 9 | volume の削除警告 | `runtime.go` `resourceInUse` | `in use` | `raw:volume-in-use-via-opossum-131.txt` | `raw:volume-in-use-131.txt` |
| 10 | image の削除警告 | `runtime.go` `DeleteImage` | `in use` | `path-tried:image-in-use-not-reached-131.txt` | `unverified` |
| 11 | `doctor` の storage 警告 | `doctor.go` `parseReclaimable` | `GB (` | `raw:doctor-inputs-131.txt` | `raw:doctor-inputs-131.txt`（`GB` と `MB` のみ。`B`/`KB`/`TB`/`PB` は unverified） |
| 12 | `doctor` の builder 警告 | `doctor.go` `parseBuilder` | `running` `MB` | `raw:doctor-inputs-131.txt` | `raw:doctor-inputs-131.txt`（`running`・`stopped`・`MB` のみ。`GB` は unverified） |

**文言列に書くのは、上流が出す文字列だけ。** コードの識別子やファイル名は「一致させている
場所」に出す。混ぜていたときは、`runtime.go` のような**コード側の名前まで「上流の文言」として
数えられていた**。

**「全部が入っていること」を検査する。** 1つでも入っていれば通す形だと、`MB` のような短い語が
**別の捕獲物のダウンロード進捗行に偶然入っていて**通ってしまう。行5 のような AND の判定は、
上流が3つのうち2つを言い換えても**1つ残っていれば緑**になる——診断が壊れる変化を、表の検査が
見逃す。

**行6〜8 の文言は、群の代表1本だけを書いている。** 各群は3本・2本・3本あり、引いたのは
1本ずつ。**残りは `unverified`**——「ほか2つ」と書いて全部引いたように読ませない。



**セルは3つのうち1つで始める。** そのあとに括弧で**到達範囲**を足してよいが、
**形が決まっている**——`（X のみ。Y は unverified）`。eval が見るのは**この形だけ**で、
**中身の広さは誰も見ていない**——同じ形のまま「実は全部確認済み」と書けば通る。形を決めたのは、
散文の留保が**セルの断定と反対のことを言える**のを止めるためであって、
「狭いことしか書けない」ようにはなっていない。

- **`raw:<ファイル>`** — `testdata/error-wordings/` に保存した生出力を指す。**そのファイルに、
  その文言が実際に入っている**
- **`path-tried:<ファイル>`** — 経路を試した記録。**届かなかった**ことの記録も含む
- **`unverified`** — 引いていない

**なぜ散文の留保ではなく、セルの3値にするか。** この表を書く過程で、私は自分の出力を「上流の
文言」として2回書いた（行8 と行11）。どちらも散文には正しい説明を書いていたのに、セルは
「確認済み」に見えたままだった。**指す生出力が無ければ `raw:` は書けない**——その形にすれば、
同じ間違いは書こうとした時点で止まる。

**`internal/runtime/wordingcite_test.go` が機械で確かめる**——セルが3値のどれかで始まること、
捕獲物が `testdata/error-wordings/` の `.txt` として実在すること、**`raw:` の捕獲物に、その行の
文言が全部入っていること**、注が**決めた形**であること、行が12あること、セルの数が
崩れた行を落とさないこと。

**捕獲物のうち、`#` と `$ ` で始まる行は証拠に数えない。** レシピやコマンドは自分で書いて
いるので、そこに一致させると**表が自分の書いたものを引用して確かめたことにする**。実際、
行12 は `builder status` を文言として挙げていたが、それは**私が書いた `$ container
builder status` の行にしか無かった**——この検査を入れて初めて分かった。

**ただしこれは「手で書いた行」と同じではない。** 落ちるのは行頭のこの2種類だけで、buildkit の
進捗行（`#1 [resolver] …`）のような**本物の出力も一緒に落ちている**（厳しい側に外れている）。
逆に、opossum 自身が印字した行（`warning: [OPSM-…]`、`hint:`）は**本文として残る**ので、
**上流の文言の証拠として通ってしまう**。`#`/`$ ` の除外が塞いだのは自己引用そのものではなく、
その一部でしかない。

(3) が要る理由：(2) だけだと**指す先を別のファイルに差し替えても通る**。実際、この eval の
最初の版はそうなっていて、しかもテストのコメントには「その文言が入っている」と書いてあった
——**テストの説明が、テストの到達範囲より広い**。この表が扱っている誤りそのものを、
その表を守るテストがやっていた。

**引き方（レシピ）は下に移した。** 表は「何を、どこまで確かめたか」だけを言う。



**行10 が「未確認」な理由**：思いついた経路では届かなかった。1.2.2 の記録は opossum の出力だけ
だったが、**1.3.1 では `container image delete` の stderr と終了コードまで採った**
（`image-in-use-not-reached-131.txt`：`--force` なしで exit 0、stderr は `Reclaimed 1.17 GB in disk
space` だけ、`in use` は無い）。**ただし「その image を running コンテナが使っていた」ことは捕獲物に
無い**——同じ採取で走っていたのは alpine の `v131recap-run` だけで、消した image のコンテナは
`--platform` の失敗で立っていない。だから「`--force` のせい」か「image 側に使用中ガードが無い」かは
**この記録からは言えない**。原因は未確認・別の経路があるかも分からない、は 1.2.2 のまま。

**行11・12 の「〜のみ」が意味すること**：一致させているのは**分岐のある形**で、上流から
引いたのはその一部だけ。行11 は `GB` と `MB`（`B`/`KB`/`TB`/`PB` は未観測——1.2.2 では `0 B (0%)`
が出ていたが 1.3.1 の捕獲物には無い）、行12 は `running`・`stopped`・`MB`（`GB` は未観測）。**分岐を列挙したまま「確認済み」と書くと、
確かめていない分岐まで確かめたことになる。**

**行6〜8 の「経路のみ」が意味すること**：`RUN` の出力に文言を書けば hint は出る——**配線は
生きている**。だが発火させたのは**こちらが書いた文字列**であって、`container` が出したもの
ではない。**上流が言い換えても、このレシピは永久に緑のまま通る。** 行8 で実際に当たったのは
alpine の busybox `dd` が出した `No space left on device` で、Apple の builder の文言ではない。

**最初はこれを行1〜5 と同じ列に「1.2.2 で確認」と書いていた。** 経路を確かめただけなのに、
上流を確かめたように見える書き方をしていた——`#477` の「誰かが確かめたはず」を、今度は
確かめた側が作っていた。列を分けたのはそのため。

生出力は `testdata/error-wordings/`。corpus 走査の2件も同じ場所に取り込んである
（リポジトリの外を指していると、eval が確かめられない）。

**6〜8 を「0件だから噛み合っていない」と読まないこと。** corpus 走査の母数（`df402-imageonly.txt`）は
`build:` を持たないプロジェクトだけで、実測で **75件中0件**。build の経路は**構造的に通らない**ので、
走査で0件でも何も分からない。区別するには **build を持つ corpus** が要る。

### レシピ：`failed to resolve … in rootfs`（OPSM-107）

```yaml
services:
  m:
    image: alpine
    volumes:
      - ./notthere.conf:/etc/passwd   # 存在しない bind source を、コンテナ内のファイルへ
    command: sleep 20
```

opossum は存在しない bind source を**ディレクトリとして作る**ので、コンテナ内のファイルパスに
載らない。

### レシピ：`Address already in use`（OPSM-201）

**同じプロジェクトの2サービスが、同じ host port を publish する。**

```yaml
services:
  a: { image: alpine, ports: ["19532:19532"], command: sleep 25 }
  b: { image: alpine, ports: ["19532:19532"], command: sleep 25 }
```

この分岐は **pre-flight が見落としたときの受け皿**。pre-flight は「**いまホストでそのアドレスが
塞がっているか**」しか訊かず、しかも**何も起動する前**に訊く。「同一プロジェクトの2サービスが
同じアドレスを要求していないか」は訊かないので、その時点ではどちらも「空いている」。両方が
通り、1本目が bind し、2本目がランタイムで失敗してここへ来る。

（同じ関数の `seen` はサービスをまたいで共有されているが、**それは原因ではない**——同じ
アドレスを2度叩かないための dedupe で、サービスごとに probe しても結果は変わらない。
その時点では誰もポートを握っていないので。）

**届かなかった道も書いておく**（次に同じ道を辿る人のために）:

- ランタイム自身の DNS が握る **53 を publish** → pre-flight が捕まえる。`netstat` でワイルドカードに
  リスナがあるので、probe が確実に落ちる
- **別プロジェクトの動いているコンテナ**と同じポート → 同じく pre-flight が捕まえる
- **loopback だけを握る TCP リスナ**（`127.0.0.1:port`）→ **pre-flight の死角は実在する**
  （Go の `net.Listen` は `SO_REUSEADDR` を立てるので、ワイルドカード probe が成功してしまう）。
  **しかしランタイム側も失敗せず、コンテナは起動する**ので、ここへは来ない。
  `port-attempt-loopback.txt` に、リスナが実在した `lsof` と probe が見えなかった出力を残してある

### レシピ：`in use` の警告（行9）

**A の named volume を、B が `external: true` で握ったまま A を `down -v`。**

```yaml
# xa/compose.yaml
services: { a: { image: alpine, volumes: ["data:/d"], command: sleep 60 } }
volumes: { data: }

# xb/compose.yaml — A の namespaced volume を実名で握る
services: { b: { image: alpine, volumes: ["av:/d"], command: sleep 60 } }
volumes: { av: { external: true, name: xa_data } }
```

`xa` で `opossum down -v`:

```
Stopping a
Removing volume xa_data
warning: could not remove volume "xa_data": it's still in use by a container — stop the container, then remove it with `container volume delete xa_data`
```

**最初は「引けない」と書いていた。** 自分のコンテナは `down` が先に止めるし、named volume は
project 名前空間なので他プロジェクトからは触れない——と考えたため。**`external: true` で実名を
書けば触れる**。「設計上塞がっている」ではなく「**思いついた経路では届かなかった**」だった。

**image 側は、この経路では届かなかった**（`image-in-use-not-reached.txt`）。別プロジェクトの
コンテナが同じ image で走っている状態（記録に `container ls` の証跡あり）で `down --rmi all`
→ `Removing image alpine`、警告なし。`DeleteImage` は `--force` を付けているが、**コードの
コメントはそれを「不在の image を無視する」意味で説明していて**、「使用中でも消える」は
この1回の実測からの推測。**別の経路があるかは分からない。**

### レシピ：build が disk full（行8）

**ホストのディスクを埋める必要はない。** `buildErrorDetector` は build のストリーム全体を
見ているので、`RUN` の出力に文言が出れば足りる:

```dockerfile
FROM alpine
RUN dd if=/dev/zero of=/dev/full bs=1M count=1
```

```
opossum: building service "b": exit status 1
hint: the build ran out of disk space — Apple's builder pulls multi-GB base images …
```

**最初は「引き方が無い（ディスクを故意に埋める必要がある）」と書いていた。** どこで文言が
出ても発火することを見ていなかった。

### レシピ：`VZErrorDomain` …（OPSM-103）

**同じ named volume を2サービスが同時に持つ。**

```yaml
services:
  a: { image: alpine, volumes: ["shared:/data"], command: sleep 25 }
  b: { image: alpine, volumes: ["shared:/data"], command: sleep 25 }
volumes: { shared: }
```

named volume の attach は排他なので、2本目が
`Error Domain=VZErrorDomain Code=2 "The storage device attachment is invalid."` で落ちる。

## `container ls -a --format json` (初出 2026-07-15、2026-08-26 に採り直し)

Array of objects; opossum's `List` reads `configuration.id` (the container
name), `status.state`, `configuration.labels`, the named volumes under
`configuration.mounts`, and `configuration.networks` — the rest is ignored.

**`configuration.networks` and `status.networks` are not the same list.** The
one under `status` is the attachments a *running* container has: a stopped
container's is empty. The one under `configuration` is what it was set up with
and stays. Anything asking "what is sitting on this network" has to read the
configuration side, or every stopped project looks like it is on nothing.

Both below are from one `container ls -a --format json` on 2026-08-26, split
into one object each and cut down (nothing was written by hand). Kept:
`configuration.id`, `configuration.labels`, `configuration.networks`,
`image.reference`, and the whole of `status` — the last because the difference
between a running entry and a stopped one is what its `networks` holds. Cut:
`initProcess`, `resources`, `platform`, `creationDate`, `capAdd`, `dns` and
their like, and also `configuration.mounts`, which `List` does read but which
has a section of its own further down. A running one:

```json
[{"configuration":{"id":"buildkit","labels":{"com.apple.container.plugin":"builder","com.apple.container.resource.role":"builder"},"image":{"reference":"ghcr.io/apple/container-builder-shim/builder:0.13.1"},"networks":[{"network":"default","options":{"hostname":"buildkit"}}]},"status":{"networks":[{"hostname":"buildkit","ipv4Address":"192.168.69.23/24","ipv4Gateway":"192.168.69.1","ipv6Address":"fde4:16e7:4f31:e6b8:f0b9:c8ff:fe41:f702/64","macAddress":"f2:b9:c8:41:f7:02","network":"default","variant":"reserved"}],"startedDate":"2026-08-22T15:32:05Z","state":"running"}}]
```

And a stopped one from the same listing — an empty `status.networks` beside a
`configuration.networks` that still names the network:

```json
[{"configuration":{"id":"cache.proj.opossum","labels":{"opossum.config-hash":"995d24914399a4f7","opossum.project":"proj"},"image":{"reference":"docker.io/library/redis:7-alpine"},"networks":[{"network":"proj-net","options":{"hostname":"cache.proj.opossum.","mtu":1280}}]},"status":{"networks":[],"startedDate":"2026-08-23T06:55:25Z","state":"stopped"}}]
```

## `container network ls --format json` (初出 2026-08-26)

Array of objects; opossum's `Networks` reads `configuration.name` and
`configuration.labels`.

Two of the five this machine had on 2026-08-26, picked for the difference
between them; the other three look like the first. Every field of the two is
here — unlike the container listing above, these objects are small.

The runtime marks what it made for itself with
`com.apple.container.resource.role: builtin` — `default`, the network the image
builder sits on. Nothing created for a project carries a label at all, so an
empty `labels` is the ordinary case rather than a missing field.

```json
[{"configuration":{"creationDate":"2026-07-22T07:47:15Z","labels":{},"mode":"nat","name":"agent-sandbox-net","options":{},"plugin":"container-network-vmnet"},"id":"agent-sandbox-net","status":{"ipv4Gateway":"192.168.65.1","ipv4Subnet":"192.168.65.0/24"}},{"configuration":{"creationDate":"2026-08-21T00:06:11Z","labels":{"com.apple.container.resource.role":"builtin"},"mode":"nat","name":"default","options":{},"plugin":"container-network-vmnet"},"id":"default","status":{"ipv4Gateway":"192.168.69.1","ipv4Subnet":"192.168.69.0/24"}}]
```

The `plugin` field names the process the runtime runs for the network:
`container-network-vmnet`, one per network, resident for as long as the network
exists.

## `container volume ls` (初出 2026-07-15)

A table with a header row and NAME/TYPE/DRIVER/OPTIONS columns (not JSON).
`VolumeExists` matches the first column of each line.

```
NAME                                  TYPE       DRIVER  OPTIONS
vz_tmp                                named      local
nmcheck                               named      local
53f55c9f-57c1-40da-bebc-6ba37f66d917  anonymous  local
```
