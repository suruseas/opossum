#!/bin/sh
# Keeps the self-hosted runner's Go build cache from growing without bound,
# and says so when the disk under the runners is running low.
#
# Private repository only: the runners keep GOCACHE on their own disk between
# jobs (setup-go's `cache:` is off there, see ci.yml) and nothing removes what
# piles up. On 2026-10-01 the disk behind the runners filled, both went
# offline and the next attempt died with `no space left on device`; on
# 2026-10-04 the cache measured 57 GiB. This is the last step of each job.
#
# **What trims.** Only the size of the cache. When GOCACHE is larger than
# OPOSSUM_CI_MAX_CACHE_GIB it deletes the cache files whose mtime is more than
# OPOSSUM_CI_TRIM_MIN_AGE_MIN minutes old. It never fails the job. The limit
# (20 GiB) is a first figure: a cap on what the cache grows to, not derived
# from any disk's free space.
#
# **What only warns.** The runners are in WSL2, whose `df` on the cache's own
# filesystem reports the virtual disk's limit (891 GiB free of 1007 GiB on
# 2026-10-04), while the virtual disk file is 106.4 GiB on a Windows drive that
# had 53 GiB free. What runs out is that drive, and `df` on its mount
# (OPOSSUM_CI_WARN_FREE_PATH, /mnt/c) does show it. Free space on it is a
# warning and never a reason to trim: deleting files inside WSL does not shrink
# the virtual disk file, so a trim would not give it back, and a floor that
# triggered one would send every job into a trim that cannot help. Below
# OPOSSUM_CI_WARN_FREE_GIB the step says so, once per job, and does nothing
# else. With no path set, or one that is not a directory or that `df` cannot
# read (a machine that has no such drive), nothing is checked and the size
# alone decides.
#
# **Why it deletes files by age and does not run `go clean -cache`.** The two
# runners share one machine, so the other job may be compiling while this one
# trims. `go clean -cache` removes the 256 subdirectories of GOCACHE, and a
# `go build` running at that moment fails on the first write into one of them
# (`open …-a: no such file or directory` — measured: 6 builds of 6, each with
# the clean looping beside it). Deleting only files, and only old ones, is
# what the go command's own trim does while other go commands run: the
# directories stay, and a file a running job is about to read is one it has
# just written or read. Age is the file's mtime, not its atime (atime is not
# dependable under WSL): the go command rewrites a used file's mtime, but only
# when it is more than an hour old, so a file whose mtime is more than 180
# minutes old was last used at least 120 minutes ago, and is not one a job of
# a run that takes a quarter of an hour can be holding. (That last step is an
# inference from the go command's source, not a measurement: deleting files of
# any age under a running build did not fail it either. It reaches as far as a
# job that does not stall: a job has no `timeout-minutes`, and one stalled for
# more than two hours can have files taken from under it.)
#
# GOMODCACHE is not touched. Its files are read by path while a build runs,
# nothing in them says whether a job still reads them, and a file deleted
# under a running build is a build that fails. It measured 24 MiB on the
# runner, so there is nothing to gain.
#
# **Who trims.** One at a time, by `flock` on OPOSSUM_CI_TRIM_LOCK: a job that
# does not get the lock does not wait and does not fail — someone else is
# trimming the same cache, and the next job's last step looks again. The lock
# is released when this process ends, so a runner that dies mid-trim does not
# leave it held.
set -u

max_gib="${OPOSSUM_CI_MAX_CACHE_GIB:-20}"
warn_gib="${OPOSSUM_CI_WARN_FREE_GIB:-20}"
warn_path="${OPOSSUM_CI_WARN_FREE_PATH:-}"
df_wait="${OPOSSUM_CI_WARN_DF_TIMEOUT_SEC:-10}"
min_age="${OPOSSUM_CI_TRIM_MIN_AGE_MIN:-180}"
lock="${OPOSSUM_CI_TRIM_LOCK:-/tmp/opossum-ci-trim.lock}"

# Whole numbers of at most 7 digits, no leading zero: `$(( ))` below reads 08
# as an error (dash ends with status 2) and 020 as octal 16, and a number of
# 14 digits overflows the multiplication into a negative one.
whole() {
	case "$1" in
	"" | *[!0-9]* | 0?*) return 1 ;;
	esac
	[ "${#1}" -le 7 ]
}
for v in "$max_gib" "$warn_gib" "$min_age" "$df_wait"; do
	if ! whole "$v"; then
		echo "::warning::ci-trim: OPOSSUM_CI_MAX_CACHE_GIB ('$max_gib'), OPOSSUM_CI_WARN_FREE_GIB ('$warn_gib'), OPOSSUM_CI_TRIM_MIN_AGE_MIN ('$min_age') and OPOSSUM_CI_WARN_DF_TIMEOUT_SEC ('$df_wait') must be whole numbers (no leading zero, at most 7 digits); not trimming"
		exit 0
	fi
done

# `/mnt/c` is a mount of the Windows drive (9p), and a drive that is stuck can
# leave `stat`/`statfs` on it waiting. A job has no `timeout-minutes`, so the
# two calls that touch it are bounded: a wait that ends is no warning, and the
# size below decides as it always does. (Without `timeout` on the machine they
# run unbounded.)
bounded() {
	if command -v timeout >/dev/null 2>&1; then
		timeout "$df_wait" "$@"
	else
		"$@"
	fi
}

# The warning, whatever the trim below does: free space on the drive under the
# virtual disk, in KiB (the 4th column of the POSIX `df -Pk`), compared in KiB.
if [ -n "$warn_path" ] && bounded test -d "$warn_path"; then
	drive_kib="$(bounded df -Pk "$warn_path" 2>/dev/null | awk 'NR == 2 && $4 ~ /^[0-9]+$/ { print $4 }')"
	if [ -n "$drive_kib" ] && [ "$drive_kib" -lt $((warn_gib * 1048576)) ]; then
		echo "::warning::ci-trim: only $((drive_kib / 1048576)) GiB is free on $warn_path (below $warn_gib GiB). Trimming the Go cache does not give it back: the virtual disk file does not shrink when files inside WSL are deleted, so this is for a person to look at"
	fi
fi

# `go env GOCACHE` says "off" for a cache that is switched off, and an empty
# string where there is no usable home directory: neither is a directory.
cache="${GOCACHE:-$(go env GOCACHE 2>/dev/null)}"
if [ -z "$cache" ] || [ ! -d "$cache" ]; then
	echo "ci-trim: no Go cache directory (${cache:-unset}); nothing to trim"
	exit 0
fi

# KiB from `du -sk`, compared in KiB: no rounding decides whether a cache of
# 20.9 GiB is over a limit of 20.
measure() {
	size_kib="$(du -sk "$cache" 2>/dev/null | awk 'NR == 1 && $1 ~ /^[0-9]+$/ { print $1 }')"
}

# After measure: succeeds when a trim is wanted. A size that could not be read
# does not ask for one.
wanted() {
	[ -n "$size_kib" ] && [ "$size_kib" -gt $((max_gib * 1048576)) ]
}

measure
[ -n "$size_kib" ] || echo "::warning::ci-trim: could not measure the size of $cache"
if ! wanted; then
	echo "ci-trim: GOCACHE is $((${size_kib:-0} / 1048576)) GiB (trims above $max_gib GiB); nothing to trim"
	exit 0
fi
before_gib=$((size_kib / 1048576))

if ! command -v flock >/dev/null 2>&1; then
	echo "::warning::ci-trim: flock is not installed on this runner; not trimming without a lock (GOCACHE is $before_gib GiB)"
	exit 0
fi
# `command exec`: a plain `exec` whose redirection fails ends a non-interactive
# dash, whatever the exit status would have been (a lock directory that is not
# there, a lock file another user made that this one may not open). The braces
# keep the silenced stderr to the redirection: a bare `exec … 2>/dev/null`
# that succeeds would send every later error to /dev/null.
if ! { command exec 9>"$lock"; } 2>/dev/null; then
	echo "::warning::ci-trim: could not open the lock file $lock; not trimming without a lock (GOCACHE is $before_gib GiB)"
	exit 0
fi
if ! flock -n 9; then
	echo "::notice::ci-trim: another job is trimming ($lock is held); skipped (GOCACHE is $before_gib GiB)"
	exit 0
fi

# The lock is ours: look again, the holder before us may have just trimmed.
measure
if ! wanted; then
	echo "ci-trim: nothing to trim now (another job trimmed first)"
	exit 0
fi

echo "ci-trim: GOCACHE is $((size_kib / 1048576)) GiB (above $max_gib GiB); deleting cache files whose mtime is more than $min_age minutes old"
# -mindepth 2 -maxdepth 2: the files directly inside the 256 subdirectories,
# never the top level (README, log.txt) and never a directory. Since Go 1.24 an
# executable of `go run`/`go tool` lives inside a `<hash>-d/` directory, and
# using it refreshes the directory's mtime but not the file's: what is inside
# one is left alone however old its own mtime says it is.
find "$cache" -mindepth 2 -maxdepth 2 -type f -mmin +"$min_age" -delete 2>/dev/null || echo "::warning::ci-trim: find reported errors; what it could delete is deleted"
measure
echo "ci-trim: GOCACHE $before_gib GiB -> $((${size_kib:-0} / 1048576)) GiB"
if wanted; then
	echo "::warning::ci-trim: still $((size_kib / 1048576)) GiB after the trim (above $max_gib GiB): what is left has an mtime within $min_age minutes, or is not an entry the trim may delete"
fi
exit 0
