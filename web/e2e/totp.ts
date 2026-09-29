import { createHmac } from 'node:crypto'

function base32Decode(input: string): Buffer {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = 0
  let value = 0
  const out: number[] = []
  for (const ch of input.replace(/\s|=/g, '').toUpperCase()) {
    const idx = alphabet.indexOf(ch)
    if (idx < 0) throw new Error(`invalid base32 character ${ch}`)
    value = (value << 5) | idx
    bits += 5
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff)
      bits -= 8
    }
  }
  return Buffer.from(out)
}

// RFC 6238 code for the current 30 s step plus an offset.
export function totp(secret: string, stepOffset = 0): string {
  const step = Math.floor(Date.now() / 1000 / 30) + stepOffset
  const msg = Buffer.alloc(8)
  msg.writeBigUInt64BE(BigInt(step))
  const sum = createHmac('sha1', base32Decode(secret)).update(msg).digest()
  const off = (sum[sum.length - 1] ?? 0) & 0x0f
  const bin = sum.readUInt32BE(off) & 0x7fffffff
  return String(bin % 1_000_000).padStart(6, '0')
}
