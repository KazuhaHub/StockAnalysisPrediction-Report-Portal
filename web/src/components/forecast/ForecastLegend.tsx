import { theme } from 'antd'
import { useTranslation } from 'react-i18next'
import { coveragePalette, forecastPalette } from '../../lib/forecastPresentation'

// What the matrix's shades and patterns mean. Two lists, because they answer two questions: the
// first says what a cell's colour claims (a significant result, or no claim, or not a result yet),
// the second, drawn only where the response carries coverage, says where a group's reports went.
// The exploratory tab words the first from p*, as its cells do.

export default function ForecastLegend({ coverage = false, custom = false }: { coverage?: boolean; custom?: boolean }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const palette = forecastPalette(token)
  const cov = coveragePalette(token)
  const list = { display: 'flex', flexWrap: 'wrap', gap: 12, listStyle: 'none', padding: 0, margin: 0, alignItems: 'center' } as const
  const swatch = { display: 'inline-block', width: 12, height: 12, borderRadius: token.borderRadiusXS, verticalAlign: -1, marginRight: 5 } as const
  const entries = [
    ['member', cov.member], ['duplicate', cov.duplicate], ['missing', cov.missing], ['other', cov.other], ['pre_live', cov.preLive],
  ] as const
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <ul aria-label={t('forecast.legend.title')} style={list}>
        {(['positive', 'negative', 'neutral'] as const).map((name) => (
          <li key={name} style={{ background: palette[name].background, color: palette[name].foreground, padding: '4px 8px', borderRadius: token.borderRadiusSM }}>
            <span aria-hidden="true">{name === 'positive' ? '▲' : name === 'negative' ? '▼' : '○'} </span>
            {t(`forecast.legend.${name}${custom && name !== 'neutral' ? '_custom' : ''}`)}
          </li>
        ))}
        <li style={{ fontSize: 12, color: token.colorTextSecondary }}>
          <span aria-hidden="true" style={{ ...swatch, border: `1px dashed ${token.colorBorder}`, background: token.colorFillQuaternary }} />
          {t('forecast.legend.open')}
        </li>
      </ul>
      {coverage && (
        <ul aria-label={t('forecast.legend.coverage_title')} style={{ ...list, fontSize: 12, color: token.colorTextSecondary }}>
          {entries.map(([name, background]) => (
            <li key={name}>
              <span aria-hidden="true" style={{ ...swatch, background }} />
              {t(`forecast.legend.cov.${name}`)}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
