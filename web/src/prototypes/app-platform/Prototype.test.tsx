import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConfigProvider } from 'antd'
import { I18nextProvider } from 'react-i18next'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { AppPlatformPrototype } from './Prototype'
import { createPrototypeLanguage, prototypeResources } from './copy'

afterEach(() => vi.unstubAllGlobals())
function fixture(
  options: {
    route?: string
    role?: 'admin' | 'reader'
    runtime?: boolean
    failure?: 'none' | 'readiness'
  } = {},
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(() => {
      throw new Error('Prototype must not call an API')
    }),
  )
  const language = createPrototypeLanguage('en-US')
  const router = createMemoryRouter(
    [
      {
        path: '*',
        element: (
          <AppPlatformPrototype
            role={options.role ?? 'admin'}
            runtimeAvailable={options.runtime ?? true}
            failure={options.failure ?? 'none'}
            stepDelay={5}
          />
        ),
      },
    ],
    { initialEntries: [options.route ?? '/apps'] },
  )
  const view = render(
    <I18nextProvider i18n={language}>
      <ConfigProvider>
        <RouterProvider router={router} />
      </ConfigProvider>
    </I18nextProvider>,
  )
  return { ...view, router, language }
}

test('the content has one app title, no nested frame, and a locale change retains the draft', async () => {
  const f = fixture({ route: '/apps/notes' }),
    user = userEvent.setup()
  expect(screen.getAllByRole('heading', { name: 'Quick Notes' })).toHaveLength(
    1,
  )
  expect(f.container.querySelector('iframe')).toBeNull()
  await user.type(screen.getByLabelText('Your note'), 'Keep this draft')
  await act(async () => {
    await f.language.changeLanguage('zh-CN')
  })
  expect(
    screen.getByLabelText(prototypeResources['zh-CN'].translation.notesLabel),
  ).toHaveProperty('value', 'Keep this draft')
  expect(
    screen.queryByRole('combobox', {
      name: prototypeResources['zh-CN'].translation.language,
    }),
  ).toBeNull()
  expect(fetch).not.toHaveBeenCalled()
})

test('managed install requires permission review, rejects duplicate submission and opens setup', async () => {
  fixture({ route: '/market/digest' })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Install' }))
  const review = await screen.findByRole('dialog', {
    name: 'Review Daily Digest',
  })
  expect(
    within(review).getByRole('checkbox', {
      name: 'Allow notification publication',
    }),
  ).toHaveProperty('checked', false)
  expect(screen.queryByText('Downloading verified package')).toBeNull()
  await user.click(
    within(review).getByRole('button', { name: 'Confirm installation' }),
  )
  await screen.findByText('Operation completed')
  await user.click(
    within(
      screen.getByRole('dialog', { name: 'Application operation' }),
    ).getByRole('button', { name: 'Close' }),
  )
  await user.click(screen.getByRole('button', { name: 'Set up' }))
  await user.type(screen.getByLabelText('Digest title'), 'Example digest')
  await user.click(screen.getByRole('button', { name: 'Finish setup' }))
  await screen.findByText('Sample background is ready')
  expect(fetch).not.toHaveBeenCalled()
})

test('runtime absence explains and disables managed installation', () => {
  fixture({ route: '/market/digest', runtime: false })
  expect(screen.getByRole('button', { name: 'Install' })).toHaveProperty(
    'disabled',
    true,
  )
  expect(
    screen.getByText(/This server needs a compatible app runtime/),
  ).toBeTruthy()
})

test('failed backend startup never exposes a usable or configured application', async () => {
  fixture({ route: '/market/digest', failure: 'readiness' })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Install' }))
  await user.click(
    await screen.findByRole('button', { name: 'Confirm installation' }),
  )
  await screen.findByText('The simulated operation failed')
  expect(screen.queryByRole('button', { name: 'Set up' })).toBeNull()
  expect(screen.queryByText('Sample background is ready')).toBeNull()
})

test('a search without matches has an explicit empty state', async () => {
  fixture({ route: '/market' })
  const user = userEvent.setup()
  await user.type(
    screen.getByRole('searchbox', { name: 'Search applications' }),
    'no matching example',
  )
  expect(screen.getByText('No matching apps')).toBeTruthy()
})

test('reader has no management or install actions and a hidden app deep link reveals no content', () => {
  fixture({ role: 'reader', route: '/apps/digest' })
  expect(screen.queryByRole('button', { name: 'Manage apps' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
  expect(
    screen.getByRole('heading', {
      name: 'This application is not available for your role',
    }),
  ).toBeTruthy()
  expect(screen.queryByLabelText('Digest title')).toBeNull()
})

test('uninstall retains sample data; deletion needs a separate typed confirmation', async () => {
  fixture({ route: '/manage' })
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: 'Uninstall Quick Notes' }),
  )
  await user.click(
    await screen.findByRole('button', { name: 'Uninstall and retain data' }),
  )
  await screen.findByText('Operation completed')
  await user.click(
    within(
      screen.getByRole('dialog', { name: 'Application operation' }),
    ).getByRole('button', { name: 'Close' }),
  )
  await screen.findByRole('heading', { name: 'Retained application data' })
  await user.click(screen.getByRole('button', { name: 'Install' }))
  const reinstallReview = await screen.findByRole('dialog', {
    name: 'Review Quick Notes',
  })
  expect(
    within(reinstallReview).getByRole('button', {
      name: 'Confirm installation',
    }),
  ).toHaveProperty('disabled', true)
  expect(
    within(reinstallReview).getByRole('checkbox', {
      name: 'I reviewed reusing the retained data',
    }),
  ).toHaveProperty('checked', false)
  expect(
    within(reinstallReview).getByRole('checkbox', { name: 'Internal OU' }),
  ).toHaveProperty('checked', false)
  await user.click(
    within(reinstallReview).getByRole('button', { name: 'Cancel' }),
  )
  await user.click(
    screen.getByRole('button', { name: 'Delete data Quick Notes' }),
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'Delete retained sample data?',
  })
  expect(
    within(dialog).getByRole('button', { name: 'Delete sample data' }),
  ).toHaveProperty('disabled', true)
  await user.type(
    within(dialog).getByLabelText('Application name'),
    'Quick Notes',
  )
  await user.click(
    within(dialog).getByRole('button', { name: 'Delete sample data' }),
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('heading', { name: 'Retained application data' }),
    ).toBeNull(),
  )
})

test('navigation protects an unsaved draft, and cancellation keeps the active page', async () => {
  const f = fixture({ route: '/apps/notes' }),
    user = userEvent.setup()
  await user.type(screen.getByLabelText('Your note'), 'Unsaved')
  await user.click(screen.getByRole('button', { name: 'App market' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Leave this draft?',
  })
  await user.click(within(dialog).getByRole('button', { name: 'Keep editing' }))
  expect(f.router.state.location.pathname).toBe('/apps/notes')
  expect(screen.getByLabelText('Your note')).toHaveProperty('value', 'Unsaved')
})

test('all preview languages have matching keys and interpolation placeholders', () => {
  const base = prototypeResources['en-US'].translation
  for (const { translation } of Object.values(prototypeResources)) {
    expect(Object.keys(translation).sort()).toEqual(Object.keys(base).sort())
    for (const key of Object.keys(base) as (keyof typeof base)[]) {
      expect(translation[key].length).toBeGreaterThan(0)
      expect(translation[key].match(/{{\w+}}/g) ?? []).toEqual(
        base[key].match(/{{\w+}}/g) ?? [],
      )
    }
  }
})
