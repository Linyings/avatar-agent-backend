// Copy into frontend/src/features/agent for the review run.
import {afterEach,beforeEach,expect,it,vi} from 'vitest'
import {cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react'
import {MantineProvider} from '@mantine/core'
import {MemoryRouter} from 'react-router-dom'
import type {ReactNode} from 'react'
import StudioHome from './StudioHome'

const api=vi.hoisted(()=>({create:vi.fn(),upload:vi.fn()}))
vi.mock('./Studio.module.css',()=>({default:{}}))
vi.mock('@/features/auth',()=>({useAuth:()=>({user:{id:'review-user'}})}))
vi.mock('@/features/projects',()=>({ProjectService:{getProjects:async()=>({projects:[]})}}))
vi.mock('./studioService',()=>({createStudio:api.create,uploadStudioFile:api.upload}))
vi.mock('./StudioShell',()=>({
 StudioShell:({children}:{children:ReactNode})=><>{children}</>,
 PackageLibrary:()=>null,
 ProjectArtwork:()=>null,
 MoreLink:()=>null,
}))
beforeEach(()=>{
 vi.clearAllMocks()
 Object.defineProperty(document,'fonts',{configurable:true,value:{addEventListener:vi.fn(),removeEventListener:vi.fn()}})
 api.create.mockResolvedValue({id:'isolated-draft'});api.upload.mockRejectedValue(new Error('文件无法解码或不符合图片/视频要求'))
})
afterEach(cleanup)

it('allows removing an invalid attachment after project creation succeeds but upload fails',async()=>{
 const {container}=render(<MantineProvider><MemoryRouter><StudioHome/></MemoryRouter></MantineProvider>)
 fireEvent.change(screen.getByRole('textbox',{name:'描述视频创意'}),{target:{value:'讲解科学知识'}})
 const input=container.querySelector('input[type="file"]')!
 fireEvent.change(input,{target:{files:[new File(['invalid-image'],'bad.svg',{type:'image/svg+xml'})]}})
 fireEvent.click(screen.getByRole('button',{name:'发送创意'}))
 await waitFor(()=>expect(api.upload).toHaveBeenCalledTimes(1))
 await screen.findByText('文件无法解码或不符合图片/视频要求')
 // Retry must permit correcting a permanently invalid file, without a reload.
 expect(screen.getByRole('button',{name:'移除bad.svg'})).toBeEnabled()
})
