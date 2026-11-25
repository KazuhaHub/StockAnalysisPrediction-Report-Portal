import { act, fireEvent, render, screen, cleanup } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import AppPageHeader from './AppPageHeader'
import { createPageActions } from '../../lib/page-v2'
import { mockNarrowViewport } from '../../test/narrow'
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
afterEach(cleanup)
test('keeps signed identity visible and renders page strings as text with common Ant actions',()=>{
  const execute=vi.fn(), actions=createPageActions({execute,publish:()=>{}})
  actions.setPage({title:'<img src=x>',description:'Recent records',actions:[{id:'save',label:'Save',variant:'primary',disabled:false,loading:false}]})
  const view=render(<AppPageHeader name="Signed application" icon={<span>Icon</span>} onBack={()=>{}} state={actions.snapshot()}/>)
  expect(screen.getByRole('heading',{name:'<img src=x>'})).toBeTruthy()
  expect(screen.getByText('Signed application')).toBeTruthy()
  expect(screen.getByText('Recent records')).toBeTruthy()
  expect(view.container.querySelector('img')).toBeNull()
  fireEvent.click(screen.getByRole('button',{name:'Save'}))
  expect(execute).toHaveBeenCalledTimes(1)
  view.rerender(<AppPageHeader name="Signed application" icon={null} onBack={()=>{}} state={actions.snapshot()}/>)
  expect((screen.getByRole('button',{name:'Save'}) as HTMLButtonElement).disabled).toBe(true)
  actions.close()
})
test('falls back to signed name with no page and exposes a fixed uncertain result',()=>{
  const actions=createPageActions({execute:()=>{throw Error('secret')},publish:()=>{}})
  actions.setPage({title:'',description:'',actions:[{id:'delete',label:'Delete',variant:'danger',disabled:false,loading:false}]})
  actions.snapshot()!.invoke('delete')
  const view=render(<AppPageHeader name="Signed application" icon={null} onBack={()=>{}} state={actions.snapshot()}/>)
  expect(screen.getByRole('heading',{name:'Signed application'})).toBeTruthy()
  expect(screen.getByRole('alert').textContent).toContain('apps.v2ActionUncertain')
  expect(view.container.textContent).not.toContain('secret')
  actions.close()
})
test('Back is shown only when the page has a way back, and an extra control sits in the header with the actions',()=>{
  const back=vi.fn()
  const view=render(<AppPageHeader name="Signed application" icon={null} state={null} extra={<button>Manage</button>}/>)
  expect(screen.queryByRole('button',{name:/apps.back/})).toBeNull()
  expect(screen.getByRole('button',{name:'Manage'})).toBeTruthy()
  view.rerender(<AppPageHeader name="Signed application" icon={null} state={null} onBack={back}/>)
  fireEvent.click(screen.getByRole('button',{name:/apps.back/}));expect(back).toHaveBeenCalledOnce()
  expect(screen.queryByRole('button',{name:'Manage'})).toBeNull()
})
test('on a wide screen, lets the title group wrap below Back instead of squeezing the title',()=>{
  const view=render(<AppPageHeader name="Market Monitor" icon={<span>Icon</span>} onBack={()=>{}} state={null}/>)
  const back=screen.getByRole('button',{name:/apps.back/})
  const heading=screen.getByRole('heading',{name:'Market Monitor'})
  const row=back.parentElement as HTMLElement
  expect(row.className).toContain('ant-flex-wrap-wrap')
  // The icon and title travel together as one item of that row, with a basis that forces a wrap before a squeeze.
  const group=[...row.children].find(child=>child.contains(heading)) as HTMLElement
  expect(group).not.toBe(back)
  expect(group.contains(screen.getByText('Icon'))).toBe(true)
  expect(group.style.flex).toMatch(/1 1 (1[6-9]\d|2\d\d)px/)
  view.unmount()
})
describe('on a phone',()=>{
  const viewport=mockNarrowViewport(true)
  test('stacks Back, the title and the actions on their own rows',()=>{
    const view=render(<AppPageHeader name="Market Monitor" icon={<span>Icon</span>} onBack={()=>{}} state={null} extra={<button>Manage</button>}/>)
    const stack=screen.getByTestId('app-page-header-stacked')
    expect(stack.className).toContain('ant-flex-vertical')
    const rows=[...stack.children]
    expect(rows).toHaveLength(3)
    expect(rows[0]).toBe(screen.getByRole('button',{name:/apps.back/}))
    expect(rows[1].contains(screen.getByRole('heading',{name:'Market Monitor'}))).toBe(true)
    expect(rows[1].contains(screen.getByText('Icon'))).toBe(true)
    expect((rows[1] as HTMLElement).style.width).toBe('100%')
    expect(rows[2].contains(screen.getByRole('button',{name:'Manage'}))).toBe(true)
    // Crossing back to a wide viewport restores the single row.
    act(()=>viewport.set(false))
    expect(screen.queryByTestId('app-page-header-stacked')).toBeNull()
    view.unmount()
  })
})
