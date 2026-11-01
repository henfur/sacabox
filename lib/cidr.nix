# Tiny IPv4 CIDR arithmetic so network policies can be expressed as pure
# allow-lists ("the internet minus private ranges, plus these exceptions").
{ lib }:
let
  inherit (lib) foldl' concatMap splitString elemAt length head toInt mod concatStringsSep;

  pow2 = n: if n == 0 then 1 else 2 * pow2 (n - 1);

  parseIp = s: foldl' (acc: o: acc * 256 + toInt o) 0 (splitString "." s);
  fmtIp = n: concatStringsSep "." (map (sh: toString (mod (n / pow2 sh) 256)) [ 24 16 8 0 ]);

  parse = c:
    let
      p = splitString "/" c;
      len = if length p > 1 then toInt (elemAt p 1) else 32;
      size = pow2 (32 - len);
      ip = parseIp (head p);
    in
    { base = ip - mod ip size; inherit len size; };

  fmt = r: "${fmtIp r.base}/${toString r.len}";

  # r \ h, both aligned CIDR blocks.
  subtract = r: h:
    if h.base + h.size <= r.base || r.base + r.size <= h.base then [ r ]
    else if h.size >= r.size then [ ]
    else
      let
        half = r.size / 2;
        len = r.len + 1;
      in
      subtract { inherit len; inherit (r) base; size = half; } h
      ++ subtract { inherit len; base = r.base + half; size = half; } h;
in
{
  # complement: carve out `holes` (list of CIDR strings) from `from` (list of CIDR strings).
  carve = from: holes:
    map fmt (foldl' (acc: h: concatMap (r: subtract r h) acc) (map parse from) (map parse holes));
}
