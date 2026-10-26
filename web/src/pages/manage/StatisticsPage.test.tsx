import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { it, expect, vi } from 'vitest'
import StatisticsPage from './StatisticsPage'
const {get,t} = vi.hoisted(() => ({get:vi.fn(),t:(key:string)=>key}))
vi.mock('../../api/client', () => ({api:{get},errText:()=> 'Failed'}))
vi.mock('react-i18next', () => ({useTranslation:()=>({t})}))
it('queries statistics independently and exports the applied filters across pages',async()=>{
 get.mockResolvedValue({items:[{label:'Research',count:12}],total:51,records:400})
 render(<App><StatisticsPage /></App>)
 expect(await screen.findByText('Research')).toBeTruthy()
 expect(get.mock.calls[0][0]).toContain('/api/admin/statistics?dataset=reports')
 const link=screen.getByRole('link',{name:/statistics.download/})
 expect(link.getAttribute('href')).toContain('format=csv')
 fireEvent.change(screen.getByRole('searchbox'),{target:{value:'Research'}})
 fireEvent.click(screen.getByRole('button',{name:/statistics.apply/}))
 await waitFor(()=> expect(get.mock.lastCall![0]).toContain('q=Research'))
 expect(screen.getByRole('link',{name:/statistics.download/}).getAttribute('href')).toContain('q=Research')
})
