import { useTranslation } from 'react-i18next'
import WarnTag from './WarnTag'

// The mark that says a group or a drawer belongs to a custom range and not to the record. It is the
// one thing between an exploratory number and a citation of it, so it has to be readable.
export default function ExploratoryTag() {
  const { t } = useTranslation()
  return <WarnTag>{t('forecast.tag.exploratory')}</WarnTag>
}
