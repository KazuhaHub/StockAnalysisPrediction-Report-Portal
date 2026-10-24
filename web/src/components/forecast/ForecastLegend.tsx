import { theme } from 'antd'
import { useTranslation } from 'react-i18next'
import { forecastPalette } from '../../lib/forecastPresentation'

export default function ForecastLegend() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const palette = forecastPalette(token)
  return (
    <ul aria-label={t('forecast.legend.title')} style={{ display: 'flex', flexWrap: 'wrap', gap: 12, listStyle: 'none', padding: 0, margin: 0 }}>
      {(['positive', 'negative', 'neutral'] as const).map((name) => (
        <li key={name} style={{ background: palette[name].background, color: palette[name].foreground, padding: '4px 8px', borderRadius: 4 }}>
          <span aria-hidden="true">{name === 'positive' ? '▲' : name === 'negative' ? '▼' : '○'} </span>
          {t(`forecast.legend.${name}`)}
        </li>
      ))}
    </ul>
  )
}
