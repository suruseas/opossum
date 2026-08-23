# df478: 診断が一致させている文言を、手で引いた記録

`container CLI version 1.2.2`（Homebrew formula `1.2.2_1`）/ 2026-08-23。
corpus 走査（df476）では発火しなかったものを、**corpus の外で引いた**もの。
引き方は `testdata/real-cli-output.md` の「診断が一致させている文言と、その引き方」に対応。

| ファイル | 引いた文言 | 出た診断 |
|---|---|---|
| `rootfs-resolve.txt` | `failed to resolve '…' in rootfs` | OPSM-107 |
| `port-in-use-duplicate-publish.txt` | `Address already in use` | OPSM-201（**文言一致の側**） |
| `vzerror-shared-named-volume.txt` | `VZErrorDomain` + `Code=2` + `storage device attachment is invalid` | OPSM-103 |
| `platform-image-index-no-arm64.txt` | `Error: platform linux/arm64` | OPSM-412 |
| `port-attempt-53.txt` | （届かず） | — |
| `port-attempt-loopback.txt` | （届かず） | — |
| `volume-in-use.txt` | `in use` | ランタイムの文言（`container volume delete` を直接） |
| `volume-in-use-via-opossum.txt` | `in use` | **opossum の警告**（`could not remove volume …`） |
| `image-in-use-not-reached.txt` | （この経路では届かず） | — |
| `build-disk-full.txt` | `No space left on device`（**busybox が出したもの**） | build の disk full hint（**経路のみ**） |
| `build-cache-path-only.txt` | `unable to read root manifest`（**自分で書いたもの**） | build の cache hint（**経路のみ**） |
| `build-resource-path-only.txt` | `rpc error: code = Unavailable`（**自分で書いたもの**） | build の resource hint（**経路のみ**） |
| `corpus-cs2-old-wording.txt` | `does not support required platforms` | OPSM-412（corpus 走査 df476 から） |
| `corpus-atlas-old-wording.txt` | 同上 | 同上 |
| `doctor-inputs.txt` | `system df` の RECLAIMABLE / `builder status` の state・memory | doctor の storage / builder 警告 |

## 届かなかった記録を残す理由

**`port-attempt-53.txt`**：ランタイム自身の DNS が握る 53 を publish しても、pre-flight が
先に捕まえる。`netstat` の証跡を同じファイルに入れてある（ワイルドカードにリスナがある）。

**注意**：`port-attempt-loopback.txt` のうち、`lsof` と opossum の出力は生出力だが、
`ワイルドカード listen: 成功` の行は**probe プログラム自身のメッセージ**で、ランタイムや
opossum の出力ではない。`SO_REUSEADDR` が効いている、という機構の帰属もこのファイルには
証跡が無い（socket option を出力していない）。結論（この経路は届かない）は機構に依らないが、
**どの行が生出力でどの行が説明かは分けて読むこと**。

**`port-attempt-loopback.txt`**：pre-flight の死角そのものは実在する（loopback だけを握る
TCP リスナは、`net.Listen("tcp4", ":port")` のワイルドカード probe から見えない——Go が
`SO_REUSEADDR` を立てるため）。同じファイルに **`lsof` でリスナが実在した証跡**と、**probe が
見えなかった出力**を入れてある。**しかしランタイム側も失敗せず、コンテナは起動する。**

つまり「pre-flight の死角」と「その死角が失敗を生む」は別で、**この経路は届かない**。
届いたのは重複 publish のほう。**試して届かなかった記録も残す**——次に同じ道を辿る人が、
同じところで時間を使わないように。

## 「経路のみ」の3件について

`RUN` の出力に文言を書けば hint は出る——**配線は生きている**。だが発火させたのは
**こちらが書いた文字列**であって、`container` が出したものではない。**上流が言い換えても、
この3件は永久に緑のまま通る。** 上流の文言を確かめたことにはならない。
