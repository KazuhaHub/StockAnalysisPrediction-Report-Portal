import { Alert, Descriptions, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../../prefs'
import ReleaseDigest from '../../components/ReleaseDigest'
import { appDisplayText, publisherWithId } from '../../lib/appDisplayV2'
import { joinList } from '../../lib/joinList'
import type { PlatformRelease } from '../../api/appPlatform'
import { formatBytes } from './AppNetworkProxyPanel'
import type { Review, Subject } from './platformLifecycle'

/** True when the review cannot be confirmed: an update whose quota raise the runner has no room for. */
export const reviewBlocked = (review: Review | null) => review?.review.action === 'update' && review.review.data_quota?.capacity === 'short'

/** The verified review of one lifecycle plan: what changes, which permissions, resources and data effect. */
export default function PlatformReviewDetails({ review, release, groups, expired }: { review: Review; release?: PlatformRelease; groups: Subject[]; expired: boolean }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const audienceLabel = (groupId: string) => groups.find(g => g.id === groupId)?.name ?? `${t('appPlatform.ou')}: ${groupId}`
  const capabilityLabel = (capability: string) => t(`appPlatform.capability.${capability}`, { defaultValue: capability })
  const updateReview = review.review.action === 'update' ? review.review : null
  const enableReview = review.review.action === 'enable' ? review.review : null
  const repairReview = review.review.action === 'repair' ? review.review : null
  const uninstallReview = review.review.action === 'uninstall' ? review.review : null
  const disableReview = review.review.action === 'disable' ? review.review : null
  const quotaRaise = updateReview?.data_quota
  const quotaShort = quotaRaise?.capacity === 'short'
  const updateDiff = updateReview ? (() => {
    const previous = updateReview.previous_capabilities ?? []
    const target = updateReview.target_capabilities ?? []
    return {
      added: target.filter(capability => !previous.includes(capability)),
      removed: previous.filter(capability => !target.includes(capability)),
    }
  })() : null
  const permissionList = (values?: string[]) => values?.length ? joinList(values.map(capabilityLabel), lang) : t('appPlatform.none')
  const compatibility = release?.compatibility ? `${release.compatibility.platform_api} · ${release.compatibility.runner_protocol}` : '—'
  const resources = release?.backend && release.resources ? t('appPlatform.resourceSummary', {
    platforms: release.platforms?.length ? release.platforms.join(', ') : t('appPlatform.none'),
    memory: release.resources.memory_mib,
    cpu: release.resources.cpu_millis,
    pids: release.resources.pids,
  }) : release ? t('appPlatform.frontendOnly') : '—'
  const declined = review.review.action === 'install' ? review.review.declined_capabilities : review.review.action === 'update' ? review.review.declined_capabilities : []
  // Doc 174 section 4: background egress needs no new review, so the review that grants background.run and
  // network.http together says what that means. A capability is granted when the release declares it and the
  // review does not decline it; only an install or an update grants.
  const grantsCapabilities = review.review.action === 'install' || review.review.action === 'update'
  const grants = (capability: string) => grantsCapabilities && !declined.includes(capability) &&
    (release?.required_capabilities?.includes(capability) || release?.optional_capabilities?.includes(capability)) === true
  const backgroundNetwork = grants('background.run') && grants('network.http')
  const backgroundCredentials = grants('background.run') && grants('secrets.use')
  return <div role="document" tabIndex={0} className="rp-platform-review rp-platform-review-scroll"><Space orientation="vertical" style={{ width: '100%' }}><Descriptions column={1} items={[
    { key: 'action', label: t('appPlatform.action'), children: t(review.review.action === 'update' ? 'appPlatform.update' : review.review.action === 'disable' ? 'appPlatform.disable' : review.review.action === 'uninstall' ? 'appPlatform.uninstall' : review.review.action === 'enable' ? 'appPlatform.enable' : review.review.action === 'repair' ? 'appPlatform.repair' : 'appPlatform.install') },
    { key: 'app', label: t('appPlatform.application'), children: `${appDisplayText(review.review.app_id, release?.display, lang).name} · ${release?.version ?? ''}` },
    { key: 'publisher', label: t('appPlatform.publisher'), children: publisherWithId(release?.publisher_name, review.review.publisher_id) },
    { key: 'release', label: t('appPlatform.releaseDigest'), children: <ReleaseDigest value={review.review.release_sha256} /> },
    { key: 'compatibility', label: t('appPlatform.compatibility'), children: compatibility },
    { key: 'resources', label: t('appPlatform.resources'), children: resources },
    ...(review.review.action === 'install' ? [{ key: 'audience', label: t('appPlatform.audience'), children: review.review.open_to_ous.length ? joinList(review.review.open_to_ous.map(audienceLabel), lang) : t('appPlatform.noAudience') }] : review.review.action === 'update' || review.review.action === 'enable' || review.review.action === 'repair' ? [{ key: 'generation', label: t('appPlatform.generation'), children: `${review.review.source_generation} → ${review.review.target_generation}` }] : [{ key: 'generation', label: t('appPlatform.generation'), children: `${review.review.generation} → ${review.review.target_generation}` }]),
    ...(uninstallReview ? [] : [
      { key: 'requiredPermissions', label: t('appPlatform.requiredPermissions'), children: permissionList(release?.required_capabilities) },
      { key: 'optionalPermissions', label: t('appPlatform.optionalPermissions'), children: permissionList(release?.optional_capabilities) },
      ...(declined.length ? [{ key: 'declinedPermissions', label: t('appPlatform.declinedPermissions'), children: permissionList(declined) }] : []),
      ...(backgroundNetwork ? [{ key: 'backgroundNetwork', label: t('appPlatform.backgroundNetwork'), children: t('appPlatform.backgroundNetworkNote') }] : []),
      ...(backgroundCredentials ? [{ key: 'backgroundCredentials', label: t('appPlatform.backgroundCredentials'), children: t('appPlatform.backgroundCredentialsNote') }] : []),
      ...(updateDiff ? [{ key: 'permissionChanges', label: t('appPlatform.permissionDiffLabel'), children: t('appPlatform.permissionDiff', { added: permissionList(updateDiff.added), removed: permissionList(updateDiff.removed) }) }] : []),
    ]),
    ...(quotaRaise ? [{
      key: 'dataQuota', label: t('appPlatform.updateQuota'), children: <Space orientation="vertical" size={4}>
        <span>{t('appPlatform.updateQuotaRaise', {
          currentBytes: formatBytes(Number(quotaRaise.current_bytes)), newBytes: formatBytes(Number(quotaRaise.new_bytes)),
          currentInodes: new Intl.NumberFormat(lang).format(BigInt(quotaRaise.current_inodes)), newInodes: new Intl.NumberFormat(lang).format(BigInt(quotaRaise.new_inodes)),
        })}</span>
        <Typography.Text type="secondary">{t('appPlatform.updateQuotaHint')}</Typography.Text>
        {quotaShort && <Alert type="error" showIcon data-testid="update-quota-short" title={t('appPlatform.settings.raiseQuotaShort', {
          dimension: quotaRaise.capacity_dimension ?? '—', requested: quotaRaise.capacity_requested ?? '—', remaining: quotaRaise.capacity_remaining ?? '—',
        })} />}
      </Space>,
    }] : []),
    { key: 'data', label: t('appPlatform.storage'), children: uninstallReview ? t(uninstallReview.managed_data_cleared ? 'appPlatform.uninstallManagedData' : 'appPlatform.uninstallPluginData') : disableReview ? t('appPlatform.disableDataRetained') : review.review.action === 'update' ? t('appPlatform.dataSchemaRetained', { schema: review.review.declared_data_schema }) : enableReview ? t('appPlatform.enableDataRetained') : repairReview ? t('appPlatform.repairDataRetained') : t('appPlatform.noData') },
  ]} /><Alert type={expired ? 'warning' : 'info'} title={t(expired ? 'err.app_platform_plan_expired' : uninstallReview ? 'appPlatform.uninstallConfirmHint' : disableReview ? 'appPlatform.disableConfirmHint' : review.review.action === 'update' ? 'appPlatform.updateConfirmHint' : repairReview ? 'appPlatform.repairConfirmHint' : enableReview ? 'appPlatform.enableConfirmHint' : 'appPlatform.confirmHint')} /></Space></div>
}
