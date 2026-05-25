#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$ROOT_DIR/.env"
EXAMPLE_ENV="$ROOT_DIR/.env.example"

# ── Colors ─────────────────────────────────────────────────────────────────────
if [[ -t 2 && "${NO_COLOR:-}" == "" ]]; then
  B='\033[1m' D='\033[2m' N='\033[0m'
  GRN='\033[32m' YLW='\033[33m' CYN='\033[36m' RED='\033[31m'
else
  B='' D='' N=''
  GRN='' YLW='' CYN='' RED=''
fi

# Top-level display — go to stdout (terminal)
section() { printf "\n${B}${CYN}── %s${N}\n" "$1"; }
ok()      { printf "  ${GRN}✓${N}  %s\n" "$1"; }
warn()    { printf "  ${YLW}⚠${N}  %s\n" "$1"; }
info()    { printf "  ${D}%s${N}\n" "$1"; }
sep()     { printf "${D}────────────────────────────────────────────────────${N}\n"; }

# Prompt display — MUST go to stderr so command substitution $() captures only the value
_p()  { printf "$@" >&2; }   # display line (stderr)
_w()  { printf "  ${YLW}⚠${N}  %s\n" "$1" >&2; }   # warning inside prompts

# ── .env file helpers ──────────────────────────────────────────────────────────
_env_get() {
  local file="$1" key="$2"
  [[ -f "$file" ]] || return 0
  grep -E "^${key}=" "$file" 2>/dev/null | tail -n1 | cut -d= -f2- || true
}
existing()     { _env_get "$ENV_FILE"    "$1"; }
from_example() { _env_get "$EXAMPLE_ENV" "$1"; }

default_for() {
  local key="$1" fallback="${2:-}"
  local v; v="$(existing "$key")"
  [[ -z "$v" ]] && v="$(from_example "$key")"
  printf '%s' "${v:-$fallback}"
}

# ── Input helpers (all display → stderr, return value → stdout) ────────────────
ask() {
  local key="$1" label="$2" fallback="${3:-}"
  local def; def="$(default_for "$key" "$fallback")"
  _p "    ${YLW}%-42s${N} [%s]: " "$label" "$def"
  local v; read -r v
  printf '%s' "${v:-$def}"
}

ask_secret() {
  # Like ask but masks existing value and auto-generates when empty
  local key="$1" label="$2"
  local def; def="$(default_for "$key" "")"
  [[ -z "$def" ]] && def="$(gen_secret)"
  local show="${def:0:6}…"
  _p "    ${YLW}%-42s${N} [%s]: " "$label" "$show"
  local v; read -r v
  printf '%s' "${v:-$def}"
}

ask_bool() {
  local key="$1" label="$2" fallback="${3:-true}"
  local def; def="$(default_for "$key" "$fallback")"
  local yn; [[ "$def" == "true" ]] && yn="y" || yn="n"
  while true; do
    _p "    ${YLW}%-42s${N} (y/n) [%s]: " "$label" "$yn"
    local v; read -r v; v="${v:-$yn}"
    case "$v" in
      y|Y|yes|true)  printf 'true';  return ;;
      n|N|no|false)  printf 'false'; return ;;
      *)             _w "Enter y or n." ;;
    esac
  done
}

ask_yn() {
  # Writes prompt to stderr, returns 0 (yes) or 1 (no)
  local label="$1" default="${2:-y}"
  while true; do
    _p "\n  ${YLW}%s${N} (y/n) [%s]: " "$label" "$default"
    local v; read -r v; v="${v:-$default}"
    case "$v" in
      y|Y|yes) return 0 ;;
      n|N|no)  return 1 ;;
      *)       _w "Enter y or n." ;;
    esac
  done
}

choose() {
  # Displays a numbered menu (stderr), returns chosen key (stdout).
  # Usage: var="$(choose "Label" "default_key" "key1:Desc" "key2:Desc" ...)"
  local label="$1" default="$2"; shift 2
  local -a keys=() descs=()
  local def_n=1 item
  for item in "$@"; do
    keys+=("${item%%:*}")
    descs+=("${item#*:}")
  done
  for i in "${!keys[@]}"; do
    [[ "${keys[$i]}" == "$default" ]] && def_n=$(( i + 1 ))
  done
  while true; do
    _p "\n  ${B}%s${N}\n" "$label"
    for i in "${!keys[@]}"; do
      local n=$(( i + 1 ))
      if [[ "${keys[$i]}" == "$default" ]]; then
        _p "    ${GRN}%d)${N} ${B}%-16s${N}  %s\n" "$n" "${keys[$i]}" "${descs[$i]}"
      else
        _p "    %d) %-16s  %s\n" "$n" "${keys[$i]}" "${descs[$i]}"
      fi
    done
    _p "\n    Choice [%d]: " "$def_n"
    local v; read -r v; v="${v:-$def_n}"
    if [[ "$v" =~ ^[0-9]+$ ]] && (( v >= 1 && v <= ${#keys[@]} )); then
      printf '%s' "${keys[$(( v - 1 ))]}"; return
    fi
    local k
    for k in "${keys[@]}"; do
      [[ "$k" == "$v" ]] && { printf '%s' "$k"; return; }
    done
    _w "Enter a number 1–${#keys[@]}."
  done
}

# ── Utils ──────────────────────────────────────────────────────────────────────
gen_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    LC_ALL=C tr -dc 'a-f0-9' </dev/urandom | head -c 64
  fi
}

check_deps() {
  section "Prerequisites"
  local failed=false

  if command -v docker >/dev/null 2>&1; then
    if docker info >/dev/null 2>&1; then
      ok "Docker: running"
    else
      warn "Docker is installed but the daemon is not running — start Docker and re-run."
      failed=true
    fi
  else
    warn "docker not found — install it: https://docs.docker.com/get-docker/"
    failed=true
  fi

  if docker compose version >/dev/null 2>&1; then
    ok "docker compose: available (V2)"
  else
    warn "docker compose V2 plugin not found."
    warn "  Install: https://docs.docker.com/compose/install/"
    failed=true
  fi

  if command -v make >/dev/null 2>&1; then
    ok "make: available"
  else
    warn "make not found — install build-essential (Debian/Ubuntu) or equivalent."
    failed=true
  fi

  if [[ "$failed" == "true" ]]; then
    printf "\n${RED}${B}  Prerequisites not met. Fix the issues above and re-run ./setup.sh.${N}\n\n"
    exit 1
  fi
}

# Wait for the API healthz endpoint to respond.
# Usage: wait_for_api <port>
wait_for_api() {
  local port="$1"
  local url="http://localhost:${port}/healthz"
  local max=120 i=0

  if ! command -v curl >/dev/null 2>&1; then
    info "curl not found — skipping health check. Verify manually: $url"
    return 0
  fi

  printf "  Waiting for API"
  while (( i < max )); do
    if curl -sf "$url" >/dev/null 2>&1; then
      printf " ${GRN}ready${N}\n"
      return 0
    fi
    printf "."
    sleep 3
    (( i += 3 ))
  done
  printf " ${YLW}timed out${N}\n"
  warn "API did not respond within ${max}s — check logs: make logs"
}

detect_accel() {
  command -v nvidia-smi >/dev/null 2>&1 && { printf 'nvidia'; return; }
  [[ -e /dev/dri/renderD128 ]]          && { printf 'vaapi';  return; }
  printf 'cpu'
}

print_accel_status() {
  if command -v nvidia-smi >/dev/null 2>&1; then
    ok "NVIDIA GPU detected"
    if command -v nvidia-ctk >/dev/null 2>&1 \
        || command -v nvidia-container-runtime >/dev/null 2>&1; then
      ok "NVIDIA Container Toolkit: installed"
    else
      warn "NVIDIA Container Toolkit not found — required for nvidia mode"
    fi
    if docker info 2>/dev/null | grep -qi nvidia; then
      ok "Docker NVIDIA runtime: available"
    else
      warn "Docker NVIDIA runtime not configured"
    fi
  else
    info "NVIDIA GPU: not detected"
  fi
  if [[ -e /dev/dri/renderD128 ]]; then
    if [[ -r /dev/dri/renderD128 && -w /dev/dri/renderD128 ]]; then
      ok "VAAPI /dev/dri/renderD128: accessible"
    else
      warn "VAAPI /dev/dri/renderD128: exists but current user lacks rw access"
    fi
  else
    info "VAAPI: /dev/dri/renderD128 not found"
  fi
}

compose_files_for() {
  local mode="$1" accel="$2"
  local f
  if [[ "$mode" == "transcoder" ]]; then
    f="docker-compose.transcoder.yml"
  else
    f="docker-compose.yml"
    [[ -f "$ROOT_DIR/docker-compose.override.yml" ]] && f="$f:docker-compose.override.yml"
  fi
  case "$accel" in
    nvidia) f="$f:docker-compose.nvidia.yml" ;;
    vaapi)  f="$f:docker-compose.vaapi.yml"  ;;
  esac
  printf '%s' "$f"
}

# ══════════════════════════════════════════════════════════════════════════════
printf "${B}${CYN}┌─────────────────────────────────────────────────┐${N}\n"
printf "${B}${CYN}│           EvadePlayer  —  Setup                 │${N}\n"
printf "${B}${CYN}└─────────────────────────────────────────────────┘${N}\n"

check_deps

# ══ 1. Deploy mode ════════════════════════════════════════════════════════════
cur_mode="$(default_for DEPLOY_MODE "all-in-one")"

mode="$(choose "Deploy mode" "$cur_mode" \
  "all-in-one:API + DB + transcoder all on this server" \
  "main:API & infra only — transcoder runs on a separate server" \
  "transcoder:Transcoder only — connects to a remote main server")"

# ══ 2. Remote server addresses (transcoder mode) ══════════════════════════════
if [[ "$mode" == "transcoder" ]]; then
  section "Remote main server"
  info "Enter the address of the server running Postgres, Redis and SeaweedFS."
  echo

  main_ip="$(ask MAIN_SERVER_IP "Main server IP / hostname" "192.168.1.100")"
  echo

  pg_user="$(ask POSTGRES_USER     "Postgres user"     "evadeplayer")"
  pg_pass="$(ask POSTGRES_PASSWORD "Postgres password" "")"
  pg_db="$(ask   POSTGRES_DB       "Postgres database" "evadeplayer")"
  pg_port="$(ask DB_PORT           "Postgres port"     "5432")"
  echo

  redis_addr="$(ask  REDIS_ADDR       "Redis address"         "${main_ip}:6379")"
  swfs_master="$(ask SEAWEEDFS_MASTER "SeaweedFS master URL"  "http://${main_ip}:9333")"
  swfs_filer="$(ask  SEAWEEDFS_FILER  "SeaweedFS filer URL"   "http://${main_ip}:8888")"
fi

# ══ 3. Database (all-in-one / main) ═══════════════════════════════════════════
if [[ "$mode" != "transcoder" ]]; then
  section "Database"

  pg_user="$(ask        POSTGRES_USER     "Postgres user"     "evadeplayer")"
  pg_pass="$(ask_secret POSTGRES_PASSWORD "Postgres password")"
  pg_db="$(ask          POSTGRES_DB       "Postgres database" "evadeplayer")"
  pg_port="$(ask        DB_PORT           "Postgres port"     "5432")"

  # Inside the compose network these are always the service hostnames
  redis_addr="redis:6379"
  swfs_master="http://seaweedfs-master:9333"
  swfs_filer="http://seaweedfs-filer:8888"
fi

# ══ 4. API & Security (all-in-one / main) ════════════════════════════════════
if [[ "$mode" != "transcoder" ]]; then
  section "API"

  api_port="$(ask         API_PORT           "Port exposed on host"           "8000")"
  public_host="$(ask     PUBLIC_HOST        "Public URL (no trailing slash)" "http://localhost")"
  cors_origins="$(ask    CORS_ORIGINS       "CORS origins (* or csv list)"   "*")"
  max_upload_gb="$(ask   MAX_UPLOAD_SIZE_GB "Max upload size (GB)"           "50")"

  cur_auth_mode="$(default_for AUTH_MODE "standalone")"
  auth_mode="$(choose "Auth mode" "$cur_auth_mode" \
    "standalone:Full app — user accounts, JWT login, registration" \
    "backend:BFF mode — service key + X-User-ID header, no user accounts")"

  if [[ "$auth_mode" == "standalone" ]]; then
    auth_required="$(ask_bool AUTH_REQUIRED      "Require auth for video access"   "true")"
    allow_reg="$(ask_bool ALLOW_REGISTRATION     "Allow user self-registration"    "true")"

    cur_upload_auth="$(default_for UPLOAD_AUTH "jwt")"
    upload_auth="$(choose "Upload authorization" "$cur_upload_auth" \
      "jwt:Caller must have a valid JWT (user-facing apps)" \
      "service_key:Machine-to-machine via SERVICE_KEY header" \
      "any:No auth check on uploads")"
  else
    auth_required="false"
    allow_reg="false"
    upload_auth="service_key"
  fi

  section "Secrets"
  info "Press Enter to keep existing value or auto-generate a new one."
  echo

  if [[ "$auth_mode" == "standalone" ]]; then
    jwt_secret="$(ask_secret JWT_SECRET "JWT secret")"
  else
    jwt_secret=""
  fi
  service_key="$(ask_secret SERVICE_KEY     "Service key")"
  hls_secret="$(ask_secret HLS_TOKEN_SECRET "HLS signing secret")"
fi

# ══ 5. Transcoder ═════════════════════════════════════════════════════════════
section "Transcoder"

workers="$(ask   TRANSCODE_WORKERS             "Worker processes"            "2")"
segment_s="$(ask TRANSCODE_HLS_SEGMENT_SECONDS "HLS segment duration (s)"   "4")"
temp_dir="$(ask  TRANSCODE_TEMP_DIR            "Temp dir (inside container)" "/tmp/evadeplayer")"
codecs="$(ask    TRANSCODE_CODECS              "Codecs (comma-separated)"    "h264,h265,av1")"
qualities="$(ask TRANSCODE_QUALITIES "Qualities (comma-separated)"   "360p,720p,1080p,1440p")"


declare -A bitrate_defaults=([360p]=1000k [720p]=5000k [1080p]=8000k [1440p]=16000k [2160p]=35000k)
declare -A _q_bw=()

IFS=',' read -ra _qlist <<< "$qualities"
for _q in "${_qlist[@]}"; do
  _q="${_q// /}"
  [[ "$_q" == "original" ]] && continue
  _cur="$(existing "TRANSCODE_QUALITY_${_q^^}_BITRATE")"
  _q_bw[$_q]="${_cur:-${bitrate_defaults[$_q]:-}}"
done

_p "\n  ${D}Default video bitrates:\n"
for _q in "${_qlist[@]}"; do
  _q="${_q// /}"
  [[ "$_q" == "original" ]] && continue
  if [[ -n "${_q_bw[$_q]}" ]]; then
    _p "    %-8s → %s\n" "$_q" "${_q_bw[$_q]}"
  else
    _p "    %-8s → ${YLW}(unknown quality — no default)${N}\n" "$_q"
  fi
done
_p "${N}\n"

if ask_yn "Customize video bitrates" "n"; then
  for _q in "${_qlist[@]}"; do
    _q="${_q// /}"
    [[ "$_q" == "original" ]] && continue
    _q_bw[$_q]="$(ask "TRANSCODE_QUALITY_${_q^^}_BITRATE" "$_q video bitrate" "${_q_bw[$_q]:-}")"
  done
fi

bitrate_lines=""
for _q in "${_qlist[@]}"; do
  _q="${_q// /}"
  [[ "$_q" == "original" ]] && continue
  [[ -n "${_q_bw[$_q]}" ]] && bitrate_lines+="TRANSCODE_QUALITY_${_q^^}_BITRATE=${_q_bw[$_q]}"$'\n'
done

preview_w="$(ask TRANSCODE_PREVIEW_WIDTH       "Preview image width"         "640")"
preview_h="$(ask TRANSCODE_PREVIEW_HEIGHT      "Preview image height"        "360")"
sprite_w="$(ask  TRANSCODE_SPRITE_WIDTH        "Sprite tile width"           "320")"
sprite_h="$(ask  TRANSCODE_SPRITE_HEIGHT       "Sprite tile height"          "180")"
sprite_cols="$(ask TRANSCODE_SPRITE_COLUMNS    "Sprite columns"              "10")"
sprite_interval="$(ask TRANSCODE_SPRITE_INTERVAL_SECONDS "Sprite interval (s)" "10")"
image_stream_bw="$(ask TRANSCODE_IMAGE_STREAM_BANDWIDTH "Image stream bandwidth" "30000")"

# ══ 6. GPU acceleration ═══════════════════════════════════════════════════════
section "GPU acceleration"
print_accel_status
echo

detected_accel="$(detect_accel)"
cur_accel="$(default_for TRANSCODE_ACCEL "$detected_accel")"

accel="$(choose "Acceleration backend" "$cur_accel" \
  "cpu:Software encoding via libx264 / libx265 / libaom-av1" \
  "nvidia:NVIDIA NVENC — requires NVIDIA Container Toolkit" \
  "vaapi:Intel / AMD VAAPI — requires /dev/dri/renderD128")"

compose_file="$(compose_files_for "$mode" "$accel")"

# ══ 7. Encoding quality ═══════════════════════════════════════════════════════
section "Encoding quality"

# Load existing / defaults so re-runs preserve values without prompting.
preset="$(default_for        TRANSCODE_PRESET            slow)"
nvidia_preset="$(default_for TRANSCODE_NVIDIA_PRESET     p5)"
h264_crf="$(default_for      TRANSCODE_H264_CRF          0)"
h265_crf="$(default_for      TRANSCODE_H265_CRF          0)"
av1_crf="$(default_for       TRANSCODE_AV1_CRF           30)"
av1_cpu="$(default_for       TRANSCODE_AV1_CPU_USED      4)"
audio_rate="$(default_for    TRANSCODE_AUDIO_SAMPLE_RATE 48000)"
scene_cut="$(default_for     TRANSCODE_SCENE_CUT         false)"

case "$accel" in
  cpu)    info "Software encoding (libx264 / libx265 / libaom-av1)" ;;
  nvidia) info "Hardware encoding (NVENC)" ;;
  vaapi)  info "Hardware encoding (VAAPI) — encoder preset not applicable" ;;
esac

vbr_status="off"
[[ "$h264_crf" != "0" || "$h265_crf" != "0" ]] && vbr_status="on"

# libaom-av1 CRF is only relevant for software encoding.
_use_libaom=false
[[ "$accel" == "cpu" && "$codecs" == *"av1"* ]] && _use_libaom=true

if [[ "$_use_libaom" == "true" ]]; then
  _p "\n  ${D}preset=%-8s  VBR=%s  AV1 crf=%-3s cpu-used=%-2s  audio=%s Hz${N}\n\n" \
    "$preset" "$vbr_status" "$av1_crf" "$av1_cpu" "$audio_rate"
elif [[ "$accel" == "nvidia" ]]; then
  _p "\n  ${D}nvidia-preset=%-4s  VBR=%s  audio=%s Hz${N}\n\n" \
    "$nvidia_preset" "$vbr_status" "$audio_rate"
else
  _p "\n  ${D}preset=%-8s  VBR=%s  audio=%s Hz${N}\n\n" \
    "$preset" "$vbr_status" "$audio_rate"
fi

if ask_yn "Configure encoding quality settings" "n"; then
  echo

  if [[ "$accel" == "cpu" ]]; then
    preset="$(choose "CPU encoder preset  (libx264 / libx265)" "$preset" \
      "fast:Fast — lower quality, quicker encode" \
      "medium:Medium — balanced" \
      "slow:Slow — better compression, ~2× longer (recommended for VoD)" \
      "slower:Slower — best quality, significantly longer")"
  fi

  if [[ "$accel" == "nvidia" ]]; then
    nvidia_preset="$(choose "NVIDIA NVENC preset" "$nvidia_preset" \
      "p4:p4 — balanced speed / quality" \
      "p5:p5 — good quality  (recommended)" \
      "p6:p6 — high quality, slower" \
      "p7:p7 — best quality, slowest")"
  fi

  echo
  info "CRF (variable bitrate): instead of a fixed bitrate, the encoder targets a constant"
  info "quality level — dark/simple scenes use less data, action scenes use more."
  info "The quality bitrate you configured above becomes the maximum allowed peak."
  info "Leave at 0 to keep fixed bitrate (CBR)."
  echo
  info "Lower number = better quality and larger file. 0 = off (use fixed bitrate instead)."
  h264_crf="$(ask TRANSCODE_H264_CRF "H.264 CRF  (0=off  |  20=excellent  22=good  24=balanced)" "$h264_crf")"
  h265_crf="$(ask TRANSCODE_H265_CRF "H.265 CRF  (0=off  |  24=excellent  26=good  28=balanced)" "$h265_crf")"

  if [[ "$_use_libaom" == "true" ]]; then
    echo
    info "AV1 (software encoder) works differently from H.264/H.265: it ignores a fixed"
    info "bitrate target and always uses CRF. The quality bitrates you set above act as a"
    info "peak cap — the encoder goes lower on simple scenes, higher on complex ones."
    info "Lower number = better quality and larger file."
    av1_crf="$(ask TRANSCODE_AV1_CRF      "AV1 CRF       (28=excellent  30=good  32=balanced  36=small)" "$av1_crf")"
    info "Lower number = better compression, slower encode."
    av1_cpu="$(ask TRANSCODE_AV1_CPU_USED "AV1 cpu-used  (4=good quality  6=faster  8=fastest)" "$av1_cpu")"
  fi

  echo
  audio_rate="$(choose "Audio sample rate" "$audio_rate" \
    "48000:48000 Hz — standard for video (recommended)" \
    "44100:44100 Hz — CD standard")"
  echo
  info "Improves seek accuracy at scene transitions. Rarely noticeable, slightly larger files."
  scene_cut="$(ask_bool TRANSCODE_SCENE_CUT "Extra keyframes at scene cuts" "$scene_cut")"
fi

# ══ 8. Player ═════════════════════════════════════════════════════════════════
player_enabled="false"
if [[ "$mode" != "transcoder" ]]; then
  section "Player"
  if [[ ! -f "$ROOT_DIR/player/package.json" ]]; then
    warn "Submodule not found. To include the player, initialize it first:"
    info "  git submodule add https://github.com/Alukkart/evade-player player"
    info "  git submodule update --init --recursive"
    info "Skipping player for now."
  else
    player_enabled="$(ask_bool PLAYER_ENABLED "Include embedded player UI" "true")"
  fi
fi

compose_profiles=""
[[ "$player_enabled" == "true" ]] && compose_profiles="player"

# ══ 8. Write .env ═════════════════════════════════════════════════════════════
section "Writing .env"

if [[ -f "$ENV_FILE" ]]; then
  cp "$ENV_FILE" "${ENV_FILE}.bak"
  ok "Backed up existing .env → .env.bak"
fi

if [[ "$mode" == "transcoder" ]]; then
  cat >"$ENV_FILE" <<EOF
DEPLOY_MODE=transcoder
COMPOSE_FILE=$compose_file

# Remote main server connection
POSTGRES_USER=$pg_user
POSTGRES_PASSWORD=$pg_pass
POSTGRES_DB=$pg_db
DB_HOST=$main_ip
DB_PORT=$pg_port
DB_SSLMODE=disable

REDIS_ADDR=$redis_addr
REDIS_QUEUE_KEY=transcoding_queue

SEAWEEDFS_MASTER=$swfs_master
SEAWEEDFS_FILER=$swfs_filer

# Transcoder
TRANSCODE_WORKERS=$workers
TRANSCODE_TEMP_DIR=$temp_dir
TRANSCODE_HLS_SEGMENT_SECONDS=$segment_s
TRANSCODE_ACCEL=$accel
TRANSCODE_CODECS=$codecs
TRANSCODE_QUALITIES=$qualities
$bitrate_lines
TRANSCODE_PREVIEW_WIDTH=$preview_w
TRANSCODE_PREVIEW_HEIGHT=$preview_h
TRANSCODE_SPRITE_WIDTH=$sprite_w
TRANSCODE_SPRITE_HEIGHT=$sprite_h
TRANSCODE_SPRITE_COLUMNS=$sprite_cols
TRANSCODE_SPRITE_INTERVAL_SECONDS=$sprite_interval
TRANSCODE_IMAGE_STREAM_BANDWIDTH=$image_stream_bw

# Encoding quality
# CPU preset (libx264/libx265): ultrafast superfast veryfast faster fast medium slow slower veryslow
TRANSCODE_PRESET=$preset
# NVIDIA NVENC preset: p1 (fastest) … p7 (best quality)
TRANSCODE_NVIDIA_PRESET=$nvidia_preset
# Variable bitrate — CRF mode: quality is constant, bitrate adapts to scene complexity.
# 0 = disabled (fixed bitrate). H.264 typical: 20-24. H.265 typical: 24-28.
# When enabled, the quality's Bitrate becomes a peak cap (-maxrate), not a fixed target.
TRANSCODE_H264_CRF=$h264_crf
TRANSCODE_H265_CRF=$h265_crf
# AV1 (libaom-av1, cpu accel only): CRF range 0-63. cpu-used 0=best quality, 8=fastest.
TRANSCODE_AV1_CRF=$av1_crf
TRANSCODE_AV1_CPU_USED=$av1_cpu
# Audio sample rate in Hz. 48000 = standard for video. 44100 = CD standard.
TRANSCODE_AUDIO_SAMPLE_RATE=$audio_rate
# Insert extra keyframes at scene cuts for better seek accuracy (slightly increases file size).
TRANSCODE_SCENE_CUT=$scene_cut

COMPOSE_PROFILES=
EOF
else
  # Build auth block based on mode so the .env stays readable.
  if [[ "$auth_mode" == "standalone" ]]; then
    _auth_block="AUTH_MODE=standalone
JWT_SECRET=$jwt_secret
AUTH_REQUIRED=$auth_required
ALLOW_REGISTRATION=$allow_reg
UPLOAD_AUTH=$upload_auth"
  else
    _auth_block="AUTH_MODE=backend
# JWT_SECRET not used in backend mode"
  fi

  cat >"$ENV_FILE" <<EOF
DEPLOY_MODE=$mode
COMPOSE_FILE=$compose_file

# PostgreSQL
POSTGRES_USER=$pg_user
POSTGRES_PASSWORD=$pg_pass
POSTGRES_DB=$pg_db
DB_HOST=postgres
DB_PORT=$pg_port
DB_SSLMODE=disable

# Redis
REDIS_ADDR=$redis_addr
REDIS_QUEUE_KEY=transcoding_queue

# SeaweedFS
SEAWEEDFS_MASTER=$swfs_master
SEAWEEDFS_FILER=$swfs_filer

# Auth
$_auth_block
SERVICE_KEY=$service_key

# API
API_PORT=$api_port
API_BASE_URL=$public_host
CORS_ORIGINS=$cors_origins
MAX_UPLOAD_SIZE_GB=$max_upload_gb

# Public URL
PUBLIC_HOST=$public_host
PUBLIC_HLS_URL=$public_host/hls

# HLS signed URLs
HLS_TOKEN_SECRET=$hls_secret

# Transcoder
TRANSCODE_WORKERS=$workers
TRANSCODE_TEMP_DIR=$temp_dir
TRANSCODE_HLS_SEGMENT_SECONDS=$segment_s
TRANSCODE_ACCEL=$accel
TRANSCODE_CODECS=$codecs
TRANSCODE_QUALITIES=$qualities
$bitrate_lines
TRANSCODE_PREVIEW_WIDTH=$preview_w
TRANSCODE_PREVIEW_HEIGHT=$preview_h
TRANSCODE_SPRITE_WIDTH=$sprite_w
TRANSCODE_SPRITE_HEIGHT=$sprite_h
TRANSCODE_SPRITE_COLUMNS=$sprite_cols
TRANSCODE_SPRITE_INTERVAL_SECONDS=$sprite_interval
TRANSCODE_IMAGE_STREAM_BANDWIDTH=$image_stream_bw

# Encoding quality
# CPU preset (libx264/libx265): ultrafast superfast veryfast faster fast medium slow slower veryslow
TRANSCODE_PRESET=$preset
# NVIDIA NVENC preset: p1 (fastest) … p7 (best quality)
TRANSCODE_NVIDIA_PRESET=$nvidia_preset
# Variable bitrate — CRF mode: quality is constant, bitrate adapts to scene complexity.
# 0 = disabled (fixed bitrate). H.264 typical: 20-24. H.265 typical: 24-28.
# When enabled, the quality's Bitrate becomes a peak cap (-maxrate), not a fixed target.
TRANSCODE_H264_CRF=$h264_crf
TRANSCODE_H265_CRF=$h265_crf
# AV1 (libaom-av1, cpu accel only): CRF range 0-63. cpu-used 0=best quality, 8=fastest.
TRANSCODE_AV1_CRF=$av1_crf
TRANSCODE_AV1_CPU_USED=$av1_cpu
# Audio sample rate in Hz. 48000 = standard for video. 44100 = CD standard.
TRANSCODE_AUDIO_SAMPLE_RATE=$audio_rate
# Insert extra keyframes at scene cuts for better seek accuracy (slightly increases file size).
TRANSCODE_SCENE_CUT=$scene_cut

# Player
PLAYER_ENABLED=$player_enabled
COMPOSE_PROFILES=$compose_profiles
EOF
fi

ok "Wrote $ENV_FILE"

# ══ 8. Summary & build ════════════════════════════════════════════════════════
section "Summary"
sep
printf "  Mode      : ${B}%s${N}\n" "$mode"
printf "  Compose   : %s\n"        "$compose_file"
printf "  Accel     : ${B}%s${N}\n" "$accel"
[[ "$mode" == "transcoder" ]] && printf "  Main srv  : %s\n" "$main_ip"
[[ "$mode" != "transcoder" ]] && printf "  Auth mode : ${B}%s${N}\n" "$auth_mode"
sep

case "$accel" in
  nvidia) warn "NVIDIA mode: ensure NVIDIA Container Toolkit is installed and the Docker NVIDIA runtime is configured." ;;
  vaapi)  warn "VAAPI mode: ensure /dev/dri/renderD128 is accessible by the Docker daemon." ;;
esac

section "Build & start"

case "$mode" in
  all-in-one)
    info "Will start: all services (API, DB, SeaweedFS, transcoder, nginx)."
    if ask_yn "Build images and start now" "y"; then
      echo
      info "Building images — this may take several minutes on first run..."
      (cd "$ROOT_DIR" && make build && make up)
      echo
      wait_for_api "$api_port"
      echo
      ok "Deploy complete."
      sep
      printf "  ${B}%-14s${N} %s\n"  "Service URL:"  "$public_host"
      printf "  ${D}%-14s${N} %s\n"  "API direct:"   "http://localhost:$api_port"
      printf "  ${D}%-14s${N} %s\n"  "Logs:"         "make logs"
      printf "  ${D}%-14s${N} %s\n"  "Stop:"         "make down"
      [[ "$player_enabled" == "true" ]] && printf "  ${D}%-14s${N} %s\n" "Player:" "$public_host/player/"
      printf "  ${D}%-14s${N} %s\n"  "Swagger:"      "$public_host/swagger/"
      sep
    else
      printf "\n  Start later:\n    make build && make up\n"
    fi
    ;;
  main)
    info "Will start: all services except the transcoder."
    if ask_yn "Build images and start now" "y"; then
      echo
      info "Building images — this may take several minutes on first run..."
      (cd "$ROOT_DIR" && make build && make up-no-transcoder)
      echo
      wait_for_api "$api_port"
      echo
      ok "Deploy complete."
      sep
      printf "  ${B}%-14s${N} %s\n"  "Service URL:"  "$public_host"
      printf "  ${D}%-14s${N} %s\n"  "API direct:"   "http://localhost:$api_port"
      printf "  ${D}%-14s${N} %s\n"  "Logs:"         "make logs"
      printf "  ${D}%-14s${N} %s\n"  "Stop:"         "make down"
      [[ "$player_enabled" == "true" ]] && printf "  ${D}%-14s${N} %s\n" "Player:" "$public_host/player/"
      printf "  ${D}%-14s${N} %s\n"  "Swagger:"      "$public_host/swagger/"
      sep
      echo
      info "To set up the transcoder on a remote server:"
      info "  1. Clone the repo there"
      info "  2. ./setup.sh  →  choose 'transcoder'  →  enter this server's IP"
      info "  3. make transcoder-up"
    else
      printf "\n  Start later:\n    make build && make up-no-transcoder\n"
    fi
    ;;
  transcoder)
    local_target="transcoder-up"
    [[ "$accel" == "nvidia" ]] && local_target="transcoder-up-nvidia"
    [[ "$accel" == "vaapi"  ]] && local_target="transcoder-up-vaapi"
    info "Will build and start the transcoder container."
    if ask_yn "Build and start now" "y"; then
      echo
      info "Building image..."
      (cd "$ROOT_DIR" && make transcoder-rebuild)
      echo
      ok "Transcoder started."
      sep
      printf "  ${D}%-14s${N} %s\n"  "Logs:"  "make transcoder-logs"
      printf "  ${D}%-14s${N} %s\n"  "Stop:"  "make transcoder-down"
      sep
    else
      printf "\n  Start later:\n    make %s\n" "$local_target"
    fi
    ;;
esac

if [[ "$player_enabled" == "true" ]]; then
  sep
  printf "\n  ${B}${CYN}Iframe embed${N}\n\n"
  printf "  ${D}Get token + expires from:${N} GET /api/videos/{id} → manifest_url\n"
  printf "  ${D}(parse ?token=...&expires=... from the manifest_url query string)${N}\n\n"
  printf "  <iframe\n"
  printf "    src=\"%s/player/?id={VIDEO_ID}&token={TOKEN}&expires={EXPIRES}&codec=h264\"\n" "$public_host"
  printf "    allow=\"autoplay; fullscreen\" frameborder=\"0\"\n"
  printf "    width=\"1280\" height=\"720\">\n"
  printf "  </iframe>\n"
  sep
fi

echo
