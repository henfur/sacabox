# The `sacabox` command. Everything declarative comes from /etc/sacabox/machines.json
# (generated from services.sacabox.machines / templates); this script only reconciles Incus with it.
{ lib, pkgs, cfg, incus }:

pkgs.writeShellApplication {
  name = "sacabox";
  runtimeInputs = [ incus pkgs.jq pkgs.nix pkgs.coreutils pkgs.gnused pkgs.gnugrep ];
  text = ''
    manifest=/etc/sacabox/machines.json
    user=$(jq -r .user "$manifest")
    home=$(jq -r .home "$manifest")
    spec=
    # incus exec starts with a minimal PATH; NixOS guests keep their tools elsewhere
    gpath=/run/wrappers/bin:/run/current-system/sw/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin

    die() { echo "sacabox: $*" >&2; exit 1; }
    q() { jq -r "$1" <<<"$spec"; }
    exists() { incus info "$1" >/dev/null 2>&1; }
    running() { [ "$(incus list "^$1\$" -f csv -c s)" = RUNNING ]; }

    load_machine() {
      spec=$(jq -e --arg n "$1" '.machines[$n]' "$manifest" 2>/dev/null) \
        || die "no machine '$1' declared (add <name>/default.nix to your machines directory; declared: $(jq -r '.machines|keys|join(" ")' "$manifest"))"
    }

    usage() {
      cat <<'EOF2'
    sacabox sh NAME [cmd...]   shell as your user (creates + starts the machine if needed)
    sacabox term NAME          same, in a dedicated terminal window (services.sacabox.terminal.enable)
    sacabox up NAME            create / reconcile with the nix config, start, wait until ready
    sacabox root NAME          root shell
    sacabox ls                 machines (running and declared)
    sacabox ip NAME            IPv4 address
    sacabox reset NAME [snap]  roll back to snapshot (default: clean)
    sacabox snap NAME [snap]   (re)take snapshot (default: clean)
    sacabox provision NAME     re-run guest config (nixos-rebuild / init script / dotfiles) after editing it
    sacabox dotfiles NAME      re-copy only the dotfiles
    sacabox home NAME          re-activate only the Home Manager config
    sacabox stop|start|rm NAME
    sacabox tmp [TEMPLATE|MACHINE] [--ct]   throwaway machine from a template, deleted on exit
    sacabox spec NAME          show the resolved spec
    sacabox ui                 web UI URL
    EOF2
    }

    # ---- waiting ---------------------------------------------------------------
    wait_agent() {
      local _
      for _ in $(seq 1 240); do
        incus exec "$1" -- true >/dev/null 2>&1 && return 0
        sleep 0.5
      done
      die "$1: incus-agent never answered (image must be an agent-capable VM/container image)"
    }

    wait_cloud_init() {
      incus exec "$1" -- sh -c 'if command -v cloud-init >/dev/null; then cloud-init status --wait >/dev/null 2>&1; fi' || true
    }

    # ---- network policy --------------------------------------------------------
    has_acl() { [ "$(q '.network.acl')" != null ]; }

    ensure_acl() {
      has_acl || return 0
      incus network acl show "ws-$1" >/dev/null 2>&1 || incus network acl create "ws-$1" >/dev/null
      q '.network.acl | {description: "managed by sacabox", egress, ingress}' | incus network acl edit "ws-$1"
    }

    acl_attach() {
      has_acl || return 0
      incus config device set "$1" eth0 security.acls="ws-$1"
      incus config device set "$1" eth0 security.acls.default.egress.action=drop
      incus config device set "$1" eth0 security.acls.default.ingress.action=drop
    }

    # Provisioning (apt, nixos-rebuild) needs the network before the policy applies.
    acl_detach() {
      has_acl || return 0
      incus config device unset "$1" eth0 security.acls 2>/dev/null || true
    }

    # ---- create / reconcile ----------------------------------------------------
    create() {
      local n=$1 a=()
      local vmtype; vmtype=$(q .type)
      if [ "$vmtype" = vm ]; then
        a+=(--vm -c security.secureboot=false)
      else
        a+=(-c security.syscalls.intercept.mknod=true -c security.syscalls.intercept.setxattr=true)
        [ "$(q .nesting)" = true ] && a+=(-c security.nesting=true)
        # host uid/gid 1000 == guest 1000, so shared dirs have sane ownership
        [ "$(q '.shares|length')" -gt 0 ] && a+=(-c "raw.idmap=both 1000 1000")
      fi
      [ "$(q .autostart)" = true ] && a+=(-c boot.autostart=true)
      [ "$(q .network.policy)" != none ] && a+=(-d "eth0,network=$(q .network.bridge)")
      if [ "$(q .cloudInit)" != null ]; then
        a+=(-c "cloud-init.user-data=$(cat "$(q .cloudInit)")")
      fi
      incus init "$(q .image)" "$n" \
        -c "limits.cpu=$(q .cpu)" -c "limits.memory=$(q .memory)" -d "root,size=$(q .disk)" \
        "''${a[@]}"
      [ "$(q .network.policy)" = none ] && incus config device add "$n" eth0 none
      return 0
    }

    # Settings that may drift from the nix config: limits, shares, ssh-agent.
    apply_config() {
      local n=$1 want s sn src dst ro
      incus config set "$n" "limits.cpu=$(q .cpu)" "limits.memory=$(q .memory)"
      incus config device set "$n" root "size=$(q .disk)" 2>/dev/null \
        || echo "sacabox: warning: could not resize root disk of $n (shrinking is not supported)" >&2

      want=$(q '.shares[].name')
      for s in $(incus config device list "$n" | grep '^ws-share-' || true); do
        grep -qx "''${s#ws-share-}" <<<"$want" || incus config device remove "$n" "$s"
      done
      while IFS= read -r s; do
        sn=ws-share-$(jq -r .name <<<"$s"); src=$(jq -r .host <<<"$s"); dst=$(jq -r .guest <<<"$s")
        ro=$(jq -r 'if .readOnly then "true" else "false" end' <<<"$s")
        [ -d "$src" ] || die "share source $src does not exist"
        if incus config device get "$n" "$sn" source >/dev/null 2>&1; then
          [ "$(incus config device get "$n" "$sn" source)" = "$src" ] \
            && [ "$(incus config device get "$n" "$sn" path)" = "$dst" ] \
            && [ "$(incus config device get "$n" "$sn" readonly)" = "$( [ "$ro" = true ] && echo true || true)" ] \
            && continue
          incus config device remove "$n" "$sn"
        fi
        incus config device add "$n" "$sn" disk source="$src" path="$dst" readonly="$ro"
      done < <(q '.shares[] | tojson')

      if [ "$(q .sshAgent)" = true ]; then
        if [ -n "''${SSH_AUTH_SOCK:-}" ] && [ -S "$SSH_AUTH_SOCK" ]; then
          incus config device remove "$n" ssh-agent 2>/dev/null || true
          incus config device add "$n" ssh-agent proxy \
            listen=unix:/run/ssh-agent.sock "connect=unix:$SSH_AUTH_SOCK" bind=instance \
            uid=1000 gid=1000 mode=0600 "security.uid=$(id -u)" "security.gid=$(id -g)"
        else
          echo "sacabox: warning: sshAgent wanted but no SSH_AUTH_SOCK in this shell; skipping" >&2
        fi
      fi
    }

    # ---- guest provisioning ----------------------------------------------------
    provision_nixos() {
      local n=$1 stage i src isdir channel
      channel=$(q .nixos.channel)
      stage=$(mktemp -d)
      mkdir -p "$stage/nixos/modules"
      cp "$(q .nixos.configuration)" "$stage/nixos/configuration.nix"
      cp "$(q .nixos.guestModule)" "$stage/nixos/workstation-guest.nix"
      while IFS=$'\t' read -r i src isdir; do
        if [ "$isdir" = true ]; then
          mkdir -p "$stage/nixos/modules/$i"
          cp -rL "$src/." "$stage/nixos/modules/$i/"
        else
          cp "$src" "$stage/nixos/modules/$i.nix"
        fi
      done < <(q '.nixos.modules[] | [.idx, .src, .isDir] | @tsv')
      chmod -R u+w "$stage"
      sed -i "s/@NAME@/$n/g" "$stage/nixos/configuration.nix" "$stage/nixos/workstation-guest.nix"
      incus file push -r -p "$stage/nixos" "$n/etc/"
      rm -rf "$stage"

      local sh=/run/current-system/sw/bin/bash
      incus exec "$n" -- "$sh" -lc "nix-instantiate --find-file nixpkgs >/dev/null 2>&1 || { nix-channel --add https://channels.nixos.org/$channel nixos && nix-channel --update; }"
      incus exec "$n" -- "$sh" -lc 'nixos-rebuild switch'
    }

    run_init() {
      local f; f=$(q '.init // empty')
      [ -n "$f" ] || return 0
      incus file push "$f" "$1/root/vm-init" --mode 0700
      incus exec "$1" --env VM_NAME="$1" --env VM_USER="$user" --env VM_HOME="$home" -- /root/vm-init
    }

    apply_dotfiles() {
      local n=$1 src stage
      src=$(q '.dotfiles // empty')
      [ -n "$src" ] || return 0
      stage=$(mktemp -d)
      cp -rL "$src/." "$stage/"
      chmod -R u+w "$stage"
      tar -C "$stage" -cf - . \
        | incus exec "$n" --user 1000 --group 1000 --env PATH="$gpath" --env HOME="$home" --cwd "$home" -- tar -xf - -C "$home" --no-same-owner --no-same-permissions --overwrite
      rm -rf "$stage"
    }

    # Home Manager is evaluated + built on the host; ship the closure in over the
    # incus agent (no guest network or channels needed) and activate it as the user.
    apply_home() {
      local n=$1 h
      h=$(q '.home // empty')
      [ -n "$h" ] || return 0
      # shellcheck disable=SC2046
      nix-store --export $(nix-store -qR "$h") \
        | incus exec "$n" -- /run/current-system/sw/bin/nix-store --import >/dev/null
      incus exec "$n" --user 1000 --group 1000 --cwd "$home" \
        --env HOME="$home" --env USER="$user" \
        --env PATH="$gpath" \
        -- "$h/activate"
    }

    provision() {
      local n=$1
      running "$n" || incus start "$n"
      wait_agent "$n"
      acl_detach "$n"
      if [ "$(q .os)" = nixos ]; then provision_nixos "$n"; else wait_cloud_init "$n"; fi
      run_init "$n"
      apply_dotfiles "$n"
      apply_home "$n"
      acl_attach "$n"
    }

    bring_up() {
      local n=$1 fresh=0
      exists "$n" || { fresh=1; create "$n"; }
      apply_config "$n"
      ensure_acl "$n"
      if [ "$fresh" = 1 ]; then
        incus start "$n"
        provision "$n"
        if [ "$(q .snapshot)" = true ] && [ -z "''${VM_NO_SNAP:-}" ]; then
          incus snapshot create "$n" clean >/dev/null
        fi
      else
        running "$n" || incus start "$n"
        wait_agent "$n"
        acl_attach "$n"
      fi
      echo "$n ready"
    }

    # Full-width coloured bar (plain SGR, works in any terminal) + window title.
    banner() {
      local hex=''${2#\#} cols=''${COLUMNS:-$(tput cols 2>/dev/null || echo 80)} text
      text=$(printf ' %s  %s' "$3" "$1")
      printf '\e]2;%s\a' "$([ "$3" = enter ] && echo "[$1]" || echo "")"
      printf '\e[48;2;%d;%d;%dm\e[1;97m%-*s\e[0m\n' \
        $((16#''${hex:0:2})) $((16#''${hex:2:2})) $((16#''${hex:4:2})) "$cols" "$text"
    }

    ${lib.optionalString cfg.terminal.enable ''
    # Dedicated window: own class (for WM rules / launcher matching) and the machine's alacritty settings.
    term() {
      local opt args=(--class "sacabox,sacabox-$1" --title "$1"); load_machine "$1"
      # settings attrset -> `-o a.b.c=<toml value>` (JSON scalars/arrays are valid TOML values)
      # shellcheck disable=SC2016
      while IFS= read -r opt; do args+=(-o "$opt"); done < <(q '
        def flat($p): to_entries[] | .key as $k | .value
          | if type == "object" then flat($p + [$k]) else "\($p + [$k] | join("."))=\(tojson)" end;
        (.terminal.settings // {}) | flat([])')
      # shellcheck disable=SC2016
      SACABOX_TERM=1 setsid -f ${cfg.terminal.package}/bin/alacritty "''${args[@]}" \
        -e sh -c 'sacabox sh "$1" || { printf "\n[sacabox sh failed, press enter to close] "; read -r _; }' sh "$1"
    }
    ''}
    shell() {
      local n=$1; shift
      if ! exists "$n"; then load_machine "$n"; bring_up "$n"; fi
      running "$n" || { incus start "$n"; wait_agent "$n"; }
      # no command: the user's own login shell (fish, zsh, ...)
      # shellcheck disable=SC2016
      if [ $# -eq 0 ]; then set -- sh -c 'exec "$(getent passwd 1000 | cut -d: -f7)" -l'; fi
      local color; color=$(jq -r --arg n "$n" '.machines[$n].color // empty' "$manifest")
      if [ -t 1 ] && [ -n "$color" ] && [ -z "''${SACABOX_TERM:-}" ]; then
        banner "$n" "$color" "enter"
        trap 'banner "$n" "$color" "leave"' EXIT
      fi
      incus exec "$n" --force-interactive --user 1000 --group 1000 \
        --cwd "$home" --env HOME="$home" --env USER="$user" --env PATH="$gpath" \
        --env TERM="''${TERM:-xterm-256color}" -- "$@"
    }

    cmd=''${1:-help}; shift || true
    case $cmd in
      sh)        [ $# -ge 1 ] || { usage; exit 2; }; shell "$@" ;;
      ${lib.optionalString cfg.terminal.enable ''term)      [ $# -eq 1 ] || { usage; exit 2; }; term "$1" ;;''}
      up)        [ $# -eq 1 ] || { usage; exit 2; }; load_machine "$1"; bring_up "$1" ;;
      provision) load_machine "$1"; exists "$1" || die "$1 does not exist yet (sacabox up $1)"
                 apply_config "$1"; ensure_acl "$1"; provision "$1" ;;
      dotfiles)  load_machine "$1"; exists "$1" || die "$1 does not exist yet (sacabox up $1)"; apply_dotfiles "$1" ;;
      home)      load_machine "$1"; exists "$1" || die "$1 does not exist yet (sacabox up $1)"
                 running "$1" || { incus start "$1"; wait_agent "$1"; }; apply_home "$1" ;;
      root)      exists "$1" || { load_machine "$1"; bring_up "$1"; }
                 # shellcheck disable=SC2016
                 exec incus exec "$1" --env PATH="$gpath" -- sh -c 'exec "$(command -v bash || echo sh)" -l' ;;
      ls)        incus list -c ntsS4
                 echo; echo "declared: $(jq -r '.machines|keys|join(" ")' "$manifest")  |  templates: $(jq -r '.templates|keys|join(" ")' "$manifest")" ;;
      ip)        incus list "^$1\$" -f csv -c 4 | cut -d' ' -f1 ;;
      reset)     incus snapshot restore "$1" "''${2:-clean}"; running "$1" || incus start "$1" ;;
      snap)      incus snapshot delete "$1" "''${2:-clean}" 2>/dev/null || true
                 incus snapshot create "$1" "''${2:-clean}" ;;
      stop)      incus stop "$1" ;;
      start)     incus start "$1" ;;
      rm)        incus delete -f "$1"; incus network acl delete "ws-$1" 2>/dev/null || true ;;
      spec)      load_machine "$1"; echo "$spec" | jq . ;;
      tmp)       t=dev; ct=
                 for a in "$@"; do case $a in --ct) ct=1 ;; *) t=$a ;; esac; done
                 name="tmp-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
                 if jq -e --arg t "$t" '.templates[$t]' "$manifest" >/dev/null 2>&1; then
                   spec=$(jq --arg t "$t" '.templates[$t]' "$manifest")
                 else
                   load_machine "$t"
                 fi
                 [ -z "$ct" ] || spec=$(jq '.type = "container"' <<<"$spec")
                 trap 'incus delete -f "$name" >/dev/null 2>&1; incus network acl delete "ws-$name" >/dev/null 2>&1 || true' EXIT
                 VM_NO_SNAP=1 bring_up "$name"
                 (shell "$name") || true ;;
      ui)        echo "https://${cfg.uiHost}:8443  (or: incus webui)" ;;
      help|-h|--help) usage ;;
      *)         usage; exit 2 ;;
    esac
  '';
}
