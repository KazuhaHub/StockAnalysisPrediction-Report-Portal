import { useLayoutEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import {
  Alert,
  Button,
  Collapse,
  ConfigProvider,
  Form,
  Select,
  Space,
  theme,
} from 'antd'
import enUS from 'antd/locale/en_US'
import zhCN from 'antd/locale/zh_CN'
import zhTW from 'antd/locale/zh_TW'
import { I18nextProvider, useTranslation } from 'react-i18next'
import { createHashRouter, Navigate, RouterProvider } from 'react-router'
import { detectLang } from '../../locales'
import { AppPlatformPrototype } from './Prototype'
import { createPrototypeLanguage } from './copy'
import type { Failure, Role } from './model'

// This entry is served only through Vite's separate development HTML document.
// It is deliberately absent from the production router and build entry graph.
if (!import.meta.env.DEV)
  throw new Error('The application platform prototype is development-only')

function PreviewSurface({
  children,
  dark,
}: {
  children: React.ReactNode
  dark: boolean
}) {
  const { token } = theme.useToken(),
    { t } = useTranslation()
  useLayoutEffect(() => {
    document.title = t('previewTitle')
    document.documentElement.lang = language.resolvedLanguage ?? 'zh-CN'
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
    document.documentElement.dataset.theme = dark ? 'dark' : 'light'
    document.body.style.fontFamily = token.fontFamily
    document.documentElement.style.setProperty(
      '--prototype-border',
      token.colorBorderSecondary,
    )
    document.body.style.background = token.colorBgLayout
    document.body.style.color = token.colorText
  }, [
    token.colorBgLayout,
    token.colorText,
    token.colorBorderSecondary,
    token.fontFamily,
    dark,
    t,
  ])
  return children
}

function Scene() {
  const { t, i18n } = useTranslation()
  const [role, setRole] = useState<Role>('admin')
  const [dark, setDark] = useState(
    document.documentElement.dataset.theme === 'dark',
  )
  const [runtime, setRuntime] = useState(true)
  const [failure, setFailure] = useState<Failure>('none')
  const [session, setSession] = useState(0)
  const languageCode = i18n.resolvedLanguage ?? 'zh-CN'
  return (
    <ConfigProvider
      locale={{ 'en-US': enUS, 'zh-CN': zhCN, 'zh-TW': zhTW }[languageCode]}
      theme={{
        algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token: { fontSize: 14, borderRadius: 8 },
      }}
    >
      <PreviewSurface dark={dark}>
        <Alert banner type="info" showIcon title={t('previewNotice')} />
        <section
          className="prototype-preview-controls"
          aria-label={t('previewSettings')}
        >
          <Collapse
            ghost
            items={[
              {
                key: 'preview',
                label: t('previewSettings'),
                children: (
                  <Form layout="vertical">
                    <Space wrap align="start" size={16}>
                      <Form.Item label={t('role')} htmlFor="preview-role">
                        <Select
                          virtual={false}
                          id="preview-role"
                          value={role}
                          onChange={setRole}
                          options={[
                            { value: 'admin', label: t('admin') },
                            { value: 'reader', label: t('reader') },
                          ]}
                        />
                      </Form.Item>
                      <Form.Item label={t('mode')} htmlFor="preview-mode">
                        <Select
                          virtual={false}
                          id="preview-mode"
                          value={dark ? 'dark' : 'light'}
                          onChange={(value) => setDark(value === 'dark')}
                          options={[
                            { value: 'light', label: t('light') },
                            { value: 'dark', label: t('dark') },
                          ]}
                        />
                      </Form.Item>
                      <Form.Item
                        label={t('language')}
                        htmlFor="preview-language"
                      >
                        <Select
                          virtual={false}
                          id="preview-language"
                          value={languageCode}
                          onChange={(value) => void i18n.changeLanguage(value)}
                          options={[
                            { value: 'zh-CN', label: t('langCN') },
                            { value: 'zh-TW', label: t('langTW') },
                            { value: 'en-US', label: t('langEN') },
                          ]}
                        />
                      </Form.Item>
                      <Form.Item label={t('runtime')} htmlFor="preview-runtime">
                        <Select
                          virtual={false}
                          id="preview-runtime"
                          value={runtime ? 'available' : 'unavailable'}
                          onChange={(value) =>
                            setRuntime(value === 'available')
                          }
                          options={[
                            { value: 'available', label: t('runtimeReady') },
                            {
                              value: 'unavailable',
                              label: t('runtimeMissing'),
                            },
                          ]}
                        />
                      </Form.Item>
                      <Form.Item label={t('failure')} htmlFor="preview-failure">
                        <Select
                          virtual={false}
                          id="preview-failure"
                          value={failure}
                          onChange={setFailure}
                          options={[
                            { value: 'none', label: t('noFailure') },
                            { value: 'download', label: t('downloadFailure') },
                            {
                              value: 'readiness',
                              label: t('readinessFailure'),
                            },
                          ]}
                        />
                      </Form.Item>
                      <Form.Item label={t('previewSettings')}>
                        <Button
                          onClick={() => setSession((current) => current + 1)}
                        >
                          {t('reset')}
                        </Button>
                      </Form.Item>
                    </Space>
                  </Form>
                ),
              },
            ]}
          />
        </section>
        <AppPlatformPrototype
          key={session}
          role={role}
          runtimeAvailable={runtime}
          failure={failure}
        />
      </PreviewSurface>
    </ConfigProvider>
  )
}

const language = createPrototypeLanguage(detectLang())
const router = createHashRouter([
  { path: '/', element: <Navigate to="/apps" replace /> },
  { path: '*', element: <Scene /> },
])
const root = createRoot(document.getElementById('root')!)
root.render(
  <I18nextProvider i18n={language}>
    <RouterProvider router={router} />
  </I18nextProvider>,
)

if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    router.dispose()
    root.unmount()
  })
}
