import { useEffect, useState } from 'react'
import {
  Alert,
  Breadcrumb,
  Button,
  Card,
  Checkbox,
  Col,
  Descriptions,
  Drawer,
  Empty,
  Form,
  Input,
  Modal,
  Row,
  Space,
  Steps,
  Tag,
  Typography,
} from 'antd'
import {
  AppstoreOutlined,
  ArrowLeftOutlined,
  DownloadOutlined,
  SettingOutlined,
} from '@ant-design/icons'
import { useBlocker, useLocation, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import {
  advanceOperation,
  beginOperation,
  catalogue,
  configureDigest,
  createPrototype,
  saveNote,
  visibleApps,
  visibleStatus,
  type Access,
  type Action,
  type AppID,
  type Failure,
  type Role,
} from './model'
import './prototype.css'

type Review = {
  id: AppID
  action: 'install' | 'update'
  access: Access
  notifications: boolean
  reuseRetained: boolean
}
type Confirmation = { id: AppID; action: 'uninstall' | 'delete' | 'restore' }

export function AppPlatformPrototype({
  role = 'admin',
  runtimeAvailable = true,
  failure = 'none',
  stepDelay = 450,
}: {
  role?: Role
  runtimeAvailable?: boolean
  failure?: Failure
  stepDelay?: number
}) {
  const { t } = useTranslation()
  const navigate = useNavigate(),
    location = useLocation()
  const [model, setModel] = useState(createPrototype)
  const [query, setQuery] = useState('')
  const [review, setReview] = useState<Review | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [deleteName, setDeleteName] = useState('')
  const [progress, setProgress] = useState(false)
  const [draft, setDraft] = useState('')
  const [digestTitle, setDigestTitle] = useState('')
  const appName = (id: AppID) => t(`${id}Name`)
  const busy = model.operation?.phase === 'running'
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      currentLocation.pathname === '/apps/notes' &&
      nextLocation.pathname !== currentLocation.pathname &&
      draft !== model.note,
  )

  useEffect(() => {
    setDraft(model.note)
  }, [model.note])
  useEffect(() => {
    if (!busy) return
    const timer = setTimeout(
      () => setModel((current) => advanceOperation(current, failure)),
      stepDelay,
    )
    return () => clearTimeout(timer)
  }, [busy, model.operation?.id, model.operation?.index, failure, stepDelay])
  useEffect(() => {
    if (role !== 'admin') {
      setReview(null)
      setConfirmation(null)
      setProgress(false)
    }
  }, [role])

  const section =
    location.pathname === '/manage'
      ? 'manage'
      : location.pathname.startsWith('/market')
        ? 'market'
        : 'myApps'
  const candidate = location.pathname.split('/')[2]
  const id: AppID | undefined =
    candidate === 'notes' || candidate === 'digest' ? candidate : undefined
  const detail = Boolean(id && location.pathname === `/market/${id}`)
  const content = Boolean(id && location.pathname === `/apps/${id}`)
  const visible = visibleApps(model, role)
  const canOpen = id && visible.includes(id)
  const filteredApps = (
    section === 'market' ? (Object.keys(catalogue) as AppID[]) : visible
  ).filter((target) =>
    `${appName(target)} ${t(`${target}Description`)}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  )
  const title = detail
    ? appName(id!)
    : content
      ? canOpen
        ? appName(id!)
        : t(role === 'reader' ? 'notAllowed' : 'notOpenable')
      : t(section)

  function start(action: Action, target: AppID, decision?: Review) {
    if (role !== 'admin') return
    const next = beginOperation(
      model,
      role,
      runtimeAvailable,
      action,
      target,
      decision,
    )
    if (next === model) return
    setModel(next)
    setProgress(true)
    setReview(null)
    setConfirmation(null)
    setDeleteName('')
  }
  function openReview(target: AppID, action: 'install' | 'update') {
    if (role !== 'admin' || busy) return
    const app = model.apps[target]
    setReview({
      id: target,
      action,
      access: action === 'update' ? app.access : 'admins',
      notifications: action === 'update' ? app.notifications : false,
      reuseRetained: !app.retained,
    })
  }
  function confirm(target: AppID, action: Confirmation['action']) {
    setDeleteName('')
    setConfirmation({ id: target, action })
  }
  function stateTag(target: AppID) {
    const status = visibleStatus(model, role, target)
    if (status === 'accessRequired') return <Tag>{t('accessRequired')}</Tag>
    if (
      catalogue[target].managed &&
      !runtimeAvailable &&
      !['available', 'removed'].includes(status)
    )
      return <Tag color="warning">{t('runtimeMissing')}</Tag>
    return (
      <Tag
        color={
          status === 'ready'
            ? 'success'
            : status === 'setup'
              ? 'processing'
              : undefined
        }
      >
        {t(status)}
      </Tag>
    )
  }
  function openButton(target: AppID) {
    const app = model.apps[target]
    return (
      <Button
        type="primary"
        disabled={
          app.status === 'disabled' ||
          (catalogue[target].managed && !runtimeAvailable)
        }
        onClick={() => navigate(`/apps/${target}`)}
      >
        {t(app.status === 'setup' ? 'continueSetup' : 'open')}
      </Button>
    )
  }
  function appCard(target: AppID, market: boolean) {
    const app = model.apps[target],
      installed = visible.includes(target),
      spec = catalogue[target]
    return (
      <Col xs={24} md={12} xl={8} key={target}>
        <Card className="prototype-app-card">
          <Space align="start" size={16}>
            <span className="prototype-app-icon" aria-hidden="true">
              {spec.icon}
            </span>
            <div className="prototype-card-copy">
              <Typography.Title level={4}>{appName(target)}</Typography.Title>
              <Typography.Paragraph type="secondary">
                {t(`${target}Description`)}
              </Typography.Paragraph>
              <Space wrap>
                {stateTag(target)}
                <Typography.Text type="secondary">
                  {t(spec.managed ? 'managed' : 'static')}
                </Typography.Text>
              </Space>
            </div>
          </Space>
          <div className="prototype-card-actions">
            <Button onClick={() => navigate(`/market/${target}`)}>
              {t('details')}
            </Button>
            {installed ? (
              openButton(target)
            ) : role === 'admin' && market ? (
              <Button
                type="primary"
                icon={<DownloadOutlined />}
                aria-label={t('install')}
                disabled={busy || (spec.managed && !runtimeAvailable)}
                onClick={() => openReview(target, 'install')}
              >
                {t('install')}
              </Button>
            ) : null}
            {role === 'admin' &&
              installed &&
              app.version !== spec.version &&
              app.status !== 'disabled' && (
                <Button
                  disabled={busy}
                  onClick={() => openReview(target, 'update')}
                >
                  {t('update')}
                </Button>
              )}
          </div>
        </Card>
      </Col>
    )
  }

  return (
    <div className="prototype-app-platform">
      <header className="prototype-global-header">
        <Button
          type="text"
          icon={<AppstoreOutlined />}
          onClick={() => navigate('/apps')}
        >
          {t('portal')}
        </Button>
        <nav aria-label={t('myApps')}>
          <Space wrap>
            {(
              [
                'myApps',
                'market',
                ...(role === 'admin' ? ['manage'] : []),
              ] as const
            ).map((tab) => (
              <Button
                key={tab}
                type={section === tab ? 'primary' : 'text'}
                onClick={() => navigate(tab === 'myApps' ? '/apps' : `/${tab}`)}
              >
                {t(tab)}
              </Button>
            ))}
          </Space>
        </nav>
      </header>
      <main className="prototype-main">
        <Breadcrumb
          items={[
            {
              title: (
                <Button type="link" onClick={() => navigate('/apps')}>
                  {t('myApps')}
                </Button>
              ),
            },
            ...(detail || content ? [{ title }] : []),
          ]}
        />
        <div className="prototype-page-header">
          <div>
            <Typography.Title level={2}>{title}</Typography.Title>
            {detail && (
              <Typography.Text type="secondary">
                {t(`${id}Description`)}
              </Typography.Text>
            )}
          </div>
          {(detail || content) && (
            <Button
              icon={<ArrowLeftOutlined />}
              onClick={() => navigate(detail ? '/market' : '/apps')}
            >
              {t('back')}
            </Button>
          )}
        </div>

        {!id && ['/apps', '/market'].includes(location.pathname) && (
          <>
            <Input.Search
              aria-label={t('search')}
              placeholder={t('search')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              className="prototype-search"
            />
            <Row gutter={[20, 20]}>
              {filteredApps.map((target) =>
                appCard(target, section === 'market'),
              )}
            </Row>
            {filteredApps.length === 0 && (query || section === 'market') && (
              <Empty description={t('resultsEmpty')} />
            )}
            {visible.length === 0 && section === 'myApps' && (
              <Empty description={t('noApps')} />
            )}
            {section === 'myApps' && (
              <Button
                type="link"
                onClick={() => navigate('/market')}
                className="prototype-discover"
              >
                {t('discover')}
              </Button>
            )}
          </>
        )}

        {detail && id && (
          <Space
            orientation="vertical"
            size={24}
            className="prototype-full-width"
          >
            <Space wrap>
              <span className="prototype-app-icon" aria-hidden="true">
                {catalogue[id].icon}
              </span>
              {stateTag(id)}
              <Tag>{t('samplePackage')}</Tag>
            </Space>
            <Descriptions
              bordered
              column={{ xs: 1, sm: 2 }}
              items={[
                {
                  key: 'publisher',
                  label: t('publisher'),
                  children: t('publisherName'),
                },
                {
                  key: 'version',
                  label: t('version'),
                  children: catalogue[id].version,
                },
                {
                  key: 'backend',
                  label: t('backend'),
                  children: t(catalogue[id].managed ? 'managed' : 'static'),
                },
                {
                  key: 'resources',
                  label: t('resources'),
                  children: catalogue[id].managed
                    ? t('resourcesValue')
                    : t('static'),
                },
              ]}
            />
            {catalogue[id].managed && !runtimeAvailable && (
              <Alert type="warning" showIcon title={t('runtimeNotice')} />
            )}
            <Space wrap>
              {visible.includes(id)
                ? openButton(id)
                : role === 'admin' && (
                    <Button
                      type="primary"
                      icon={<DownloadOutlined />}
                      aria-label={t('install')}
                      disabled={
                        busy || (catalogue[id].managed && !runtimeAvailable)
                      }
                      onClick={() => openReview(id, 'install')}
                    >
                      {t('install')}
                    </Button>
                  )}
              {role === 'admin' &&
                visible.includes(id) &&
                model.apps[id].version !== catalogue[id].version &&
                model.apps[id].status !== 'disabled' && (
                  <Button
                    disabled={busy}
                    onClick={() => openReview(id, 'update')}
                  >
                    {t('update')}
                  </Button>
                )}
            </Space>
          </Space>
        )}

        {content &&
          id &&
          (!canOpen ? (
            <Alert showIcon type="info" title={t('notOpenable')} />
          ) : catalogue[id].managed && !runtimeAvailable ? (
            <Alert showIcon type="warning" title={t('runtimeNotice')} />
          ) : model.apps[id].status === 'disabled' ? (
            <Alert showIcon type="info" title={t('disabled')} />
          ) : id === 'notes' ? (
            <Form
              layout="vertical"
              onFinish={() => setModel((current) => saveNote(current, draft))}
            >
              <Form.Item label={t('notesLabel')} htmlFor="prototype-note">
                <Input.TextArea
                  id="prototype-note"
                  value={draft}
                  maxLength={10_000}
                  onChange={(event) => setDraft(event.target.value)}
                  placeholder={t('notesPlaceholder')}
                  autoSize={{ minRows: 12, maxRows: 24 }}
                />
              </Form.Item>
              <Space wrap>
                <Button
                  type="primary"
                  htmlType="submit"
                  disabled={draft === model.note}
                >
                  {t('save')}
                </Button>
                {draft === model.note && model.note && (
                  <Typography.Text role="status" type="secondary">
                    {t('noteSaved')}
                  </Typography.Text>
                )}
              </Space>
            </Form>
          ) : model.apps.digest.status === 'setup' ? (
            role === 'admin' ? (
              <Form
                layout="vertical"
                onFinish={() =>
                  setModel((current) =>
                    configureDigest(current, role, digestTitle),
                  )
                }
                className="prototype-setup"
              >
                <Typography.Paragraph type="secondary">
                  {t('digestSetupHint')}
                </Typography.Paragraph>
                <Form.Item
                  label={t('digestTitle')}
                  htmlFor="prototype-digest-title"
                >
                  <Input
                    id="prototype-digest-title"
                    maxLength={80}
                    value={digestTitle}
                    onChange={(event) => setDigestTitle(event.target.value)}
                  />
                </Form.Item>
                <Button
                  type="primary"
                  htmlType="submit"
                  disabled={!digestTitle.trim()}
                >
                  {t('setupFinish')}
                </Button>
              </Form>
            ) : (
              <Alert title={t('setup')} type="info" />
            )
          ) : (
            <Space
              orientation="vertical"
              size={20}
              className="prototype-full-width"
            >
              <Alert showIcon type="success" title={t('digestReady')} />
              <Card title={model.apps.digest.digestTitle}>
                <Empty description={t('digestSample')} />
              </Card>
            </Space>
          ))}

        {location.pathname === '/manage' && role === 'admin' && (
          <Space
            orientation="vertical"
            size={24}
            className="prototype-full-width"
          >
            {visible.map((target) => (
              <Card
                key={target}
                title={
                  <Space wrap>
                    <span>
                      {catalogue[target].icon} {appName(target)}
                    </span>
                    {stateTag(target)}
                    <Typography.Text type="secondary">
                      {model.apps[target].version}
                    </Typography.Text>
                  </Space>
                }
              >
                <Space wrap>
                  <Button
                    icon={<SettingOutlined />}
                    onClick={() => navigate(`/market/${target}`)}
                  >
                    {t('details')}
                  </Button>
                  {model.apps[target].status !== 'disabled' &&
                    model.apps[target].version !==
                      catalogue[target].version && (
                      <Button
                        disabled={busy}
                        onClick={() => openReview(target, 'update')}
                      >
                        {t('update')}
                      </Button>
                    )}
                  <Button
                    disabled={
                      busy ||
                      (model.apps[target].status === 'disabled' &&
                        catalogue[target].managed &&
                        !runtimeAvailable)
                    }
                    onClick={() =>
                      start(
                        model.apps[target].status === 'disabled'
                          ? 'enable'
                          : 'disable',
                        target,
                      )
                    }
                  >
                    {t(
                      model.apps[target].status === 'disabled'
                        ? 'enable'
                        : 'disable',
                    )}
                  </Button>
                  {model.apps[target].backup && (
                    <Button
                      disabled={busy}
                      onClick={() => confirm(target, 'restore')}
                    >
                      {t('restore')}
                    </Button>
                  )}
                  <Button
                    danger
                    disabled={busy}
                    aria-label={`${t('uninstall')} ${appName(target)}`}
                    onClick={() => confirm(target, 'uninstall')}
                  >
                    {t('uninstall')}
                  </Button>
                </Space>
              </Card>
            ))}
            {(Object.keys(catalogue) as AppID[]).some(
              (target) =>
                model.apps[target].retained &&
                model.apps[target].status === 'removed',
            ) && (
              <section>
                <Typography.Title level={4}>
                  {t('retainedData')}
                </Typography.Title>
                {(Object.keys(catalogue) as AppID[])
                  .filter(
                    (target) =>
                      model.apps[target].retained &&
                      model.apps[target].status === 'removed',
                  )
                  .map((target) => (
                    <Card key={target} title={appName(target)}>
                      <Space wrap>
                        <Tag>{t('retained')}</Tag>
                        <Button
                          disabled={
                            busy ||
                            (catalogue[target].managed && !runtimeAvailable)
                          }
                          onClick={() => openReview(target, 'install')}
                        >
                          {t('install')}
                        </Button>
                        <Button
                          danger
                          disabled={busy}
                          aria-label={`${t('delete')} ${appName(target)}`}
                          onClick={() => confirm(target, 'delete')}
                        >
                          {t('delete')}
                        </Button>
                      </Space>
                    </Card>
                  ))}
              </section>
            )}
          </Space>
        )}
        {!['/apps', '/market', '/manage'].includes(location.pathname) &&
          !detail &&
          !content && <Empty description={t('notFound')} />}
        {location.pathname === '/manage' && role !== 'admin' && (
          <Alert type="info" title={t('notAllowed')} />
        )}
      </main>

      {review && role === 'admin' && (
        <Modal
          open
          title={
            review &&
            t(review.action === 'update' ? 'updateTitle' : 'permissionTitle', {
              name: appName(review.id),
            })
          }
          onCancel={() => setReview(null)}
          onOk={() => review && start(review.action, review.id, review)}
          okText={t(
            review?.action === 'update' ? 'confirmUpdate' : 'confirmInstall',
          )}
          cancelText={t('cancel')}
          okButtonProps={{
            disabled:
              busy || (model.apps[review.id].retained && !review.reuseRetained),
          }}
          destroyOnHidden
        >
          {review && (
            <Space
              orientation="vertical"
              size={16}
              className="prototype-full-width"
            >
              {model.apps[review.id].retained && (
                <>
                  <Alert showIcon type="info" title={t('retainedReview')} />
                  <Checkbox
                    checked={review.reuseRetained}
                    onChange={(event) =>
                      setReview({
                        ...review,
                        reuseRetained: event.target.checked,
                      })
                    }
                  >
                    {t('reuseRetained')}
                  </Checkbox>
                </>
              )}
              <Typography.Text type="secondary">
                {t('requiredNote')}
              </Typography.Text>
              <Checkbox checked disabled>
                {t('storagePermission')}
              </Checkbox>
              {catalogue[review.id].managed && (
                <Checkbox checked disabled>
                  {t('backgroundPermission')}
                </Checkbox>
              )}
              <Checkbox
                checked={review.notifications}
                onChange={(event) =>
                  setReview({ ...review, notifications: event.target.checked })
                }
              >
                {t('notificationPermission')}
              </Checkbox>
              <Typography.Text type="secondary">
                {t('notificationHint')}
              </Typography.Text>
              <Form layout="vertical">
                <Form.Item label={t('access')}>
                  <Space orientation="vertical">
                    <Checkbox
                      checked={review.access === 'internal'}
                      onChange={(event) =>
                        setReview({
                          ...review,
                          access: event.target.checked ? 'internal' : 'admins',
                        })
                      }
                    >
                      {t('internal')}
                    </Checkbox>
                    <Typography.Text type="secondary">
                      {t('adminsOnly')}
                    </Typography.Text>
                  </Space>
                </Form.Item>
              </Form>
            </Space>
          )}
        </Modal>
      )}

      <Drawer
        open={progress && role === 'admin'}
        title={t('progress')}
        onClose={() => setProgress(false)}
        closable={false}
        styles={{ wrapper: { width: 'min(520px, 100vw)' } }}
        footer={
          <Button onClick={() => setProgress(false)}>{t('close')}</Button>
        }
        destroyOnHidden
      >
        {model.operation && (
          <Space
            orientation="vertical"
            size={20}
            className="prototype-full-width"
          >
            <Typography.Text strong>
              {appName(model.operation.app)}
            </Typography.Text>
            <Steps
              orientation="vertical"
              current={
                model.operation.phase === 'complete'
                  ? model.operation.stages.length
                  : model.operation.index
              }
              status={model.operation.phase === 'failed' ? 'error' : 'process'}
              items={model.operation.stages.map((stage) => ({
                title: t(`stage_${stage}`),
              }))}
            />
            {model.operation.phase === 'running' && (
              <Typography.Paragraph type="secondary">
                {t('progressHint')}
              </Typography.Paragraph>
            )}
            {model.operation.phase === 'complete' && (
              <Alert showIcon type="success" title={t('operationDone')} />
            )}
            {model.operation.phase === 'failed' && (
              <Alert
                showIcon
                type="error"
                title={t('operationFailed')}
                description={t('oldVersionSafe')}
              />
            )}
          </Space>
        )}
      </Drawer>

      {confirmation && role === 'admin' && (
        <Modal
          open
          title={
            confirmation &&
            t(`${confirmation.action}Title`, { name: appName(confirmation.id) })
          }
          onCancel={() => setConfirmation(null)}
          onOk={() =>
            confirmation &&
            (confirmation.action !== 'delete' ||
              deleteName === appName(confirmation.id)) &&
            start(confirmation.action, confirmation.id)
          }
          cancelText={t('cancel')}
          okText={t(
            confirmation?.action === 'delete'
              ? 'confirmDelete'
              : confirmation?.action === 'restore'
                ? 'confirmRestore'
                : 'confirmUninstall',
          )}
          okButtonProps={{
            danger: confirmation?.action !== 'restore',
            disabled:
              busy ||
              (confirmation?.action === 'delete' &&
                deleteName !== appName(confirmation.id)),
          }}
          destroyOnHidden
        >
          {confirmation && (
            <>
              <Typography.Paragraph>
                {t(`${confirmation.action}Hint`)}
              </Typography.Paragraph>
              {confirmation.action === 'delete' && (
                <Form layout="vertical">
                  <Form.Item
                    label={t('typeName')}
                    htmlFor="prototype-delete-name"
                  >
                    <Input
                      id="prototype-delete-name"
                      value={deleteName}
                      onChange={(event) => setDeleteName(event.target.value)}
                      autoComplete="off"
                    />
                  </Form.Item>
                </Form>
              )}
            </>
          )}
        </Modal>
      )}
      <Modal
        open={blocker.state === 'blocked'}
        title={t('leaveTitle')}
        onCancel={() => blocker.state === 'blocked' && blocker.reset()}
        onOk={() => blocker.state === 'blocked' && blocker.proceed()}
        okText={t('leave')}
        cancelText={t('keepEditing')}
      >
        <Typography.Paragraph>{t('leaveHint')}</Typography.Paragraph>
      </Modal>
    </div>
  )
}
