/**
 * IPv4 CIDR helpers used by the networking pages — wizard validation,
 * subnet first/last/count display, and overlap checks against sibling
 * subnets within a VNet.
 *
 * Stays tiny on purpose. If we ever need IPv6 or full subnet algebra,
 * pull in a library like `ip-cidr` or `netparser` instead of expanding
 * this file.
 */

export interface ParsedCidr {
  ip: string;
  prefix: number;
  /** /N → 2^(32-N) */
  totalAddresses: number;
  /** First usable host (network + 1) — undefined for /31 and /32 */
  firstHost?: string;
  /** Last usable host (broadcast - 1) — undefined for /31 and /32 */
  lastHost?: string;
  /** Network address (X.X.X.0 for /24) */
  network: string;
  /** Broadcast address (X.X.X.255 for /24) */
  broadcast: string;
}

const CIDR_RE = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/;

function octetsToInt(octets: number[]): number {
  return ((octets[0] << 24) | (octets[1] << 16) | (octets[2] << 8) | octets[3]) >>> 0;
}

function intToOctets(n: number): string {
  return [(n >>> 24) & 0xff, (n >>> 16) & 0xff, (n >>> 8) & 0xff, n & 0xff].join('.');
}

/** Returns ParsedCidr or null if the string isn't a valid /8-/32 IPv4 CIDR. */
export function parseCidr(cidr: string): ParsedCidr | null {
  const m = CIDR_RE.exec(cidr.trim());
  if (!m) return null;
  const octets = [+m[1], +m[2], +m[3], +m[4]];
  const prefix = +m[5];
  if (octets.some((o) => o < 0 || o > 255)) return null;
  if (prefix < 0 || prefix > 32) return null;

  const ipInt = octetsToInt(octets);
  const mask = prefix === 0 ? 0 : ((0xffffffff << (32 - prefix)) >>> 0);
  const network = ipInt & mask;
  const broadcast = network | (~mask >>> 0);
  const totalAddresses = 2 ** (32 - prefix);

  const out: ParsedCidr = {
    ip: cidr.trim(),
    prefix,
    totalAddresses,
    network: intToOctets(network),
    broadcast: intToOctets(broadcast),
  };
  if (prefix <= 30) {
    out.firstHost = intToOctets(network + 1);
    out.lastHost = intToOctets(broadcast - 1);
  }
  return out;
}

/**
 * Strict CIDR validation for VNets and subnets.
 * - Must parse
 * - Prefix must be in [minPrefix, maxPrefix]
 * - Must be properly aligned (network address == base of CIDR)
 * - Must be RFC1918 if requireRFC1918 is set
 */
export function validateCidr(
  cidr: string,
  opts: { minPrefix?: number; maxPrefix?: number; requireRFC1918?: boolean } = {}
): { ok: true; parsed: ParsedCidr } | { ok: false; reason: string } {
  const parsed = parseCidr(cidr);
  if (!parsed) return { ok: false, reason: 'Not a valid IPv4 CIDR (e.g. 10.10.0.0/16).' };

  const { minPrefix = 8, maxPrefix = 28 } = opts;
  if (parsed.prefix < minPrefix || parsed.prefix > maxPrefix) {
    return {
      ok: false,
      reason: `Prefix /${parsed.prefix} is outside the allowed range /${minPrefix}-/${maxPrefix}.`,
    };
  }

  // Alignment: the IP entered must equal the network address.
  const [a, b, c, d] = cidr.trim().split('/')[0].split('.').map(Number);
  if (intToOctets(octetsToInt([a, b, c, d])) !== parsed.network) {
    return {
      ok: false,
      reason: `CIDR is not aligned to its prefix. Use ${parsed.network}/${parsed.prefix}.`,
    };
  }

  if (opts.requireRFC1918) {
    const inRFC1918 =
      a === 10 ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168);
    if (!inRFC1918) {
      return {
        ok: false,
        reason: 'CIDR must be in an RFC1918 private range (10.x, 172.16-31.x, 192.168.x).',
      };
    }
  }
  return { ok: true, parsed };
}

/** Two CIDRs overlap if either's network range contains the other's start. */
export function cidrsOverlap(a: ParsedCidr, b: ParsedCidr): boolean {
  const aStart = octetsToInt(a.network.split('.').map(Number));
  const aEnd = octetsToInt(a.broadcast.split('.').map(Number));
  const bStart = octetsToInt(b.network.split('.').map(Number));
  const bEnd = octetsToInt(b.broadcast.split('.').map(Number));
  return aStart <= bEnd && bStart <= aEnd;
}

/**
 * Per-region reserved CIDRs that tenant address spaces must not overlap.
 * Mirrors `regions.reserved_cidrs` seeded in
 * `dc-api/internal/db/schema.sql`. dc-api enforces this server-side; we
 * also enforce it client-side so the wizard fails fast instead of waiting
 * for the API call to come back 400.
 *
 * F12 will replace this with `GET /v1/regions/{name}` — until then, this
 * list must be kept in sync manually if the backend seed changes.
 */
export interface ReservedCidr {
  cidr: string;
  label: string;
}

// Labels are user-facing — keep them descriptive without leaking the
// underlying technology (Harvester / RKE2 / KubeOVN). The actual CIDR
// values must match dc-api/internal/db/schema.sql exactly.
export const REGION_RESERVED_CIDRS: Record<string, ReservedCidr[]> = {
  lk: [
    { cidr: '192.168.10.0/24', label: 'datacenter management network' },
    { cidr: '10.42.0.0/16', label: 'cluster pod network' },
    { cidr: '10.43.0.0/16', label: 'cluster service network' },
  ],
};

/** Returns the first reserved CIDR that the candidate overlaps, or null. */
export function findReservedOverlap(
  candidate: ParsedCidr,
  region: string
): ReservedCidr | null {
  const list = REGION_RESERVED_CIDRS[region] ?? [];
  for (const r of list) {
    const parsed = parseCidr(r.cidr);
    if (parsed && cidrsOverlap(parsed, candidate)) return r;
  }
  return null;
}
