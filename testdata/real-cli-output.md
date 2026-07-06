# 実 `container` CLI 出力リファレンス（fake シム同期用）

opossum が出力を**パースする**コマンドについて、実 `container` 1.0.0 / macOS 26 で
採取した stdout+stderr と exit code の golden。`testdata/fake-container.sh` はこれに
合わせて出力を返し、`internal/runtime/runtime_test.go` の忠実性 eval はこの文字列を
各パーサに流して整合を確認する。CLI 更新時はここを再採取して同期すること。

採取日: 2026-07-02 / `container CLI version 1.0.0`

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

## `container inspect <name>`  （不在, exit 1）
```
Error: container not found: <name>
```
→ `InspectIP`: capture がエラーを返すため `""`（stopped 扱い）。

## `container inspect <name>`  （稼働中, exit 0）
`status.state` に状態（`running` / `stopped`）、`status.networks[].ipv4Address` に IF アドレス、
`configuration.publishedPorts[]` に公開ポート（`containerPort` / `hostAddress`（`0.0.0.0`）/
`hostPort` / `proto`）。詳細な JSON は `fake-container.sh` の inspect ケースを参照。
→ `Inspect(name)`: この1回のパースで State / IP / Ports / Labels / Exists を取り出し、
`ps` の IP・PORTS・STATUS 列と、IP/Label 系ヘルパの両方に使う。
ラベルは `configuration.labels`（マップ）。`run -l opossum.project=<name>` を付けると:
```
"configuration" : { "labels" : { "opossum.project" : "demo" }, ... }
```
→ `InspectLabel(name, key)`: これを読んでコンテナの所属プロジェクトを判定。`--dns-domain` 未設定
（bare 名）時の同名衝突ガードと所属メタデータに使う。

## DNS 解決の挙動（spike で確認 / 複数プロジェクト分離の根拠）
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
USAGE: container volume delete [--all] [--debug] [<names> ...]
  <names>   Volume names        （`delete` は `rm` エイリアスあり）
```
実機ラウンドトリップ（`container CLI version 1.0.0`）:
```
$ container volume create opossum-review-vol   # 作成
$ container volume ls                           # -> named / local として一覧
$ container volume delete opossum-review-vol    # 削除（exit 0、標準出力なし）
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
実機で確認した重要挙動: **複数コンテナ名を渡すと1テーブルにまとめて表示**し、**stopped/不在のコンテナは
グレースフルにスキップ**（running 分のみ表示、exit 0）。→ `opossum stats` は出力をパースせず passthrough
（`runtime.Stats` が `stream` で stdio 直結）するため、この出力形式に依存しない。

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
`container run --help` に **`--platform <platform>`**（マルチプラットフォーム image 用）、**`--rosetta`**（コンテナ内 x86-64 エミュ有効化）、`-a/--arch`（既定 arm64）が存在。amd64 専用 image は arm64 既定だと
`does not support required platforms` で失敗するが、`--platform linux/amd64 --rosetta` で起動可能:
```
$ container run -d --platform linux/amd64 --rosetta redislabs/redismod   # amd64 専用 image
$ container exec <name> redis-cli ping   # -> PONG（Rosetta で稼働）
```
→ `runtime.Run` は `RunOptions.Platform` があれば `--platform <p>` を発行し、`p` に `amd64`/`x86_64` を含めば
`--rosetta` も付与。`orchestrator` は compose `platform:`（`Service.Platform`）を配線。
