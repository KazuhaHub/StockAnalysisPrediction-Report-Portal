import { Tag, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import { forecastWarningText } from '../../lib/forecastPresentation'

// The mark that says a group or a drawer belongs to a custom range and not to the record. It is the
// one thing between an exploratory number and a citation of it, so it must be readable: antd's own
// warning tag measures 1.83:1 on its tint in the light theme.
export default function ExploratoryTag() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  return (
    <Tag
      style={{
        margin: 0,
        whiteSpace: 'normal',
        color: forecastWarningText(token),
        background: token.colorWarningBg,
        borderColor: token.colorWarningBorder,
      }}
    >
      {t('forecast.tag.exploratory')}
    </Tag>
  )
}
