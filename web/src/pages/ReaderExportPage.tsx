import { Result, Spin } from 'antd'
import { useTranslation } from 'react-i18next'
import { useExportPolicy } from '../lib/useExportPolicy'
import ExportPage from './manage/exports/ExportPage'

// The reader's export page (ADR 0046): the administrator's export page in reader mode. It reads the
// resolved policy first and, when export is not allowed for the reader's OU, says so in place of the
// page rather than showing controls the server would refuse.
export default function ReaderExportPage() {
  const { t } = useTranslation()
  const { policy, loaded } = useExportPolicy()
  if (!loaded) return <Spin style={{ display: 'block', margin: '64px auto' }} />
  if (!policy?.allowed) return <Result status="403" title={t('export.reader.disabledTitle')} subTitle={t('export.reader.disabledBody')} />
  return <ExportPage mode="reader" policy={policy} />
}
