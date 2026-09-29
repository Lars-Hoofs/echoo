import { splitNumber } from '../lib/format'

type NumSize = 's' | 'm' | 'l' | 'xl'

// ron's hero numeral: light weight, small raised currency, grey decimals and unit. A number is
// formatted the Dutch way; a string is shown as given (for values like a duration).
export function Num({
  value,
  fractionDigits = 0,
  currency,
  unit,
  size = 'm',
  className = '',
}: {
  value: number | string
  fractionDigits?: number
  currency?: string
  unit?: string
  size?: NumSize
  className?: string
}) {
  const { integer, decimals } = typeof value === 'number' ? splitNumber(value, fractionDigits) : { integer: value, decimals: '' }
  return (
    <span className={`num num-${size} ${className}`}>
      {currency && <span className="cur">{currency}</span>}
      {integer}
      {decimals && <span className="dec">{decimals}</span>}
      {unit && <span className="unit">{unit}</span>}
    </span>
  )
}
