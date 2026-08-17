import { useState, useEffect, useCallback } from 'react'
import {
  Card, Upload, Button, Table, Progress, App, Space, Breadcrumb, Input, Dropdown, Spin, Select,
} from 'antd'
import type { MenuProps } from 'antd'
import {
  UploadOutlined, FolderOutlined, FileOutlined, DownloadOutlined, FolderOpenOutlined,
  DeleteOutlined, EditOutlined, FolderAddOutlined, MoreOutlined, HomeOutlined,
} from '@ant-design/icons'
import { uploadFile, type UploadProgress } from '../lib/uploader'
import { downloadFile, extractDownloadError } from '../lib/downloader'
import {
  listFiles, createFolder, moveFile, renameFile, deleteFile, type FileNode,
} from '../lib/files'
import { palette } from '../theme'

// 上传中的临时行（本地 state，与后端文件列表分离）。
interface UploadRow {
  key: string
  fileId: number | null
  name: string
  size: number
  percent: number
  phase: UploadProgress['phase']
}

interface BreadcrumbItem {
  id: number | null
  name: string
}

export function FilesPage() {
  const { message, modal } = App.useApp()
  const [files, setFiles] = useState<FileNode[]>([])
  const [uploads, setUploads] = useState<UploadRow[]>([])
  const [loading, setLoading] = useState(false)
  const [downloading, setDownloading] = useState<Set<number>>(new Set())
  const [currentFolder, setCurrentFolder] = useState<number | null>(null)
  const [breadcrumb, setBreadcrumb] = useState<BreadcrumbItem[]>([{ id: null, name: '根目录' }])

  const loadFiles = useCallback(async (folderId: number | null) => {
    setLoading(true)
    try {
      const resp = await listFiles(folderId)
      setFiles(resp.files)
    } catch (e) {
      const err = e as Error
      message.error(`加载文件列表失败：${err.message}`)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => {
    loadFiles(currentFolder)
  }, [currentFolder, loadFiles])

  const handleUpload = async (file: File) => {
    const key = `${file.name}-${file.size}-${Date.now()}`
    setUploads((prev) => [
      { key, fileId: null, name: file.name, size: file.size, percent: 0, phase: 'hashing' },
      ...prev,
    ])
    try {
      const result = await uploadFile({
        file,
        parentId: currentFolder,
        onProgress: (p) => {
          setUploads((prev) =>
            prev.map((r) => (r.key === key ? { ...r, percent: p.percent, phase: p.phase } : r)),
          )
        },
      })
      setUploads((prev) =>
        prev.map((r) => (r.key === key ? { ...r, fileId: result.fileId } : r)),
      )
      message.success(result.instant ? `${file.name} 秒传成功` : `${file.name} 上传完成`)
      // 刷新文件列表
      loadFiles(currentFolder)
      // 移除上传行
      setTimeout(() => {
        setUploads((prev) => prev.filter((r) => r.key !== key))
      }, 2000)
    } catch (e) {
      const err = e as Error
      setUploads((prev) =>
        prev.map((r) => (r.key === key ? { ...r, phase: 'error', percent: 0 } : r)),
      )
      message.error(`${file.name} 上传失败：${err.message}`)
    }
    return false
  }

  const handleDownload = async (file: FileNode) => {
    setDownloading((prev) => new Set(prev).add(file.id))
    try {
      await downloadFile({ fileId: file.id, fileName: file.name })
      message.success(`${file.name} 下载已开始`)
    } catch (e) {
      const msg = await extractDownloadError(e)
      message.error(`${file.name} 下载失败：${msg}`)
    } finally {
      setDownloading((prev) => {
        const next = new Set(prev)
        next.delete(file.id)
        return next
      })
    }
  }

  const handleOpenFolder = (file: FileNode) => {
    setCurrentFolder(file.id)
    setBreadcrumb((prev) => [...prev, { id: file.id, name: file.name }])
  }

  const handleBreadcrumbClick = (index: number) => {
    const item = breadcrumb[index]
    setCurrentFolder(item.id)
    setBreadcrumb(breadcrumb.slice(0, index + 1))
  }

  const handleCreateFolder = () => {
    let name = ''
    modal.confirm({
      title: '新建文件夹',
      content: (
        <Input
          placeholder="文件夹名称"
          onChange={(e) => { name = e.target.value }}
          autoFocus
        />
      ),
      onOk: async () => {
        if (!name.trim()) {
          message.warning('请输入文件夹名称')
          return Promise.reject()
        }
        try {
          await createFolder(name.trim(), currentFolder)
          message.success('文件夹创建成功')
          loadFiles(currentFolder)
        } catch (e) {
          const err = e as Error
          message.error(`创建失败：${err.message}`)
          return Promise.reject(e)
        }
      },
    })
  }

  const handleRename = (file: FileNode) => {
    let name = file.name
    modal.confirm({
      title: '重命名',
      content: (
        <Input
          defaultValue={file.name}
          onChange={(e) => { name = e.target.value }}
          autoFocus
        />
      ),
      onOk: async () => {
        if (!name.trim()) {
          message.warning('请输入名称')
          return Promise.reject()
        }
        try {
          await renameFile(file.id, name.trim())
          message.success('重命名成功')
          loadFiles(currentFolder)
        } catch (e) {
          const err = e as Error
          message.error(`重命名失败：${err.message}`)
          return Promise.reject(e)
        }
      },
    })
  }

  const handleDelete = (file: FileNode) => {
    modal.confirm({
      title: '确认删除',
      content: `确定将「${file.name}」移入回收站？${file.is_folder ? '文件夹内的所有内容将一并删除。' : ''}`,
      okText: '删除',
      okType: 'danger',
      cancelText: '取消',
      onOk: async () => {
        try {
          await deleteFile(file.id)
          message.success('已移入回收站')
          loadFiles(currentFolder)
        } catch (e) {
          const err = e as Error
          message.error(`删除失败：${err.message}`)
        }
      },
    })
  }

  // 移动文件：列出根目录下的文件夹作为候选目标，外加"根目录"选项。
  // MVP 限制：仅支持移动到根目录或根目录下的文件夹；移动到任意子目录留待文件树组件就绪后补。
  const handleMove = async (file: FileNode) => {
    let targetParentId: number | null = null
    let destinations: FileNode[] = []
    try {
      const resp = await listFiles(null)
      destinations = resp.files.filter((f) => f.is_folder && f.id !== file.id)
    } catch (e) {
      const err = e as Error
      message.error(`加载目标文件夹失败：${err.message}`)
      return
    }
    modal.confirm({
      title: `移动「${file.name}」`,
      content: (
        <Select
          style={{ width: '100%' }}
          placeholder="选择目标文件夹"
          defaultValue={null}
          onChange={(v: number | null) => { targetParentId = v }}
          options={[
            { value: null, label: '根目录' },
            ...destinations.map((d) => ({ value: d.id, label: d.name })),
          ]}
        />
      ),
      onOk: async () => {
        try {
          await moveFile(file.id, targetParentId)
          message.success('移动成功')
          loadFiles(currentFolder)
        } catch (e) {
          const err = e as Error
          message.error(`移动失败：${err.message}`)
          return Promise.reject(e)
        }
      },
    })
  }

  // 文件操作菜单
  const getFileActions = (file: FileNode): MenuProps['items'] => {
    const items: MenuProps['items'] = []
    if (!file.is_folder) {
      items.push({
        key: 'download',
        label: '下载',
        icon: <DownloadOutlined />,
        disabled: downloading.has(file.id),
        onClick: () => handleDownload(file),
      })
    } else {
      items.push({
        key: 'open',
        label: '打开',
        icon: <FolderOpenOutlined />,
        onClick: () => handleOpenFolder(file),
      })
    }
    items.push({
      key: 'rename',
      label: '重命名',
      icon: <EditOutlined />,
      onClick: () => handleRename(file),
    })
    items.push({
      key: 'move',
      label: '移动到',
      icon: <FolderOpenOutlined />,
      onClick: () => handleMove(file),
    })
    items.push({ type: 'divider' })
    items.push({
      key: 'delete',
      label: '删除',
      icon: <DeleteOutlined />,
      danger: true,
      onClick: () => handleDelete(file),
    })
    return items
  }

  // 合并显示：上传中行 + 后端文件列表
  const tableData = [
    ...uploads.map((u) => ({
      key: u.key,
      type: 'upload' as const,
      name: u.name,
      size: u.size,
      uploadRow: u,
      file: null as FileNode | null,
    })),
    ...files.map((f) => ({
      key: `file-${f.id}`,
      type: 'file' as const,
      name: f.name,
      size: f.size,
      uploadRow: null as UploadRow | null,
      file: f,
    })),
  ]

  const columns = [
    {
      title: '文件名',
      dataIndex: 'name',
      render: (name: string, record: typeof tableData[0]) => {
        if (record.type === 'upload') {
          return (
            <Space>
              <FileOutlined style={{ color: palette.textSecondary }} />
              <span style={{ color: palette.textSecondary }}>{name}</span>
            </Space>
          )
        }
        const file = record.file!
        return (
          <Space>
            {file.is_folder ? (
              <FolderOutlined
                style={{ color: palette.accent, cursor: 'pointer' }}
                onClick={() => handleOpenFolder(file)}
              />
            ) : (
              <FileOutlined style={{ color: palette.primary }} />
            )}
            {file.is_folder ? (
              <a
                onClick={() => handleOpenFolder(file)}
                style={{ color: 'inherit', textDecoration: 'none' }}
              >
                {name}
              </a>
            ) : (
              name
            )}
          </Space>
        )
      },
    },
    {
      title: '大小',
      dataIndex: 'size',
      width: 120,
      render: (size: number, record: typeof tableData[0]) =>
        record.type === 'file' && record.file?.is_folder ? (
          <span style={{ color: palette.textSecondary }}>—</span>
        ) : (
          <span style={{ color: palette.textSecondary }}>{formatSize(size)}</span>
        ),
    },
    {
      title: '进度',
      key: 'progress',
      width: 200,
      render: (_: unknown, record: typeof tableData[0]) => {
        if (record.type === 'upload') {
          const u = record.uploadRow!
          return u.phase === 'error' ? (
            <span style={{ color: '#ef4444', fontSize: 13 }}>失败</span>
          ) : (
            <Progress
              percent={Math.round(u.percent)}
              size="small"
              strokeColor={{ from: palette.primary, to: palette.accent }}
            />
          )
        }
        const file = record.file!
        return (
          <span style={{ color: palette.textSecondary, fontSize: 13 }}>
            {file.is_folder ? '文件夹' : file.status === 'completed' ? '已完成' : file.status}
          </span>
        )
      },
    },
    {
      title: '操作',
      key: 'action',
      width: 80,
      render: (_: unknown, record: typeof tableData[0]) => {
        if (record.type === 'upload') {
          return null
        }
        const file = record.file!
        return (
          <Dropdown menu={{ items: getFileActions(file) }} trigger={['click']}>
            <Button type="text" size="small" icon={<MoreOutlined />} />
          </Dropdown>
        )
      },
    },
  ]

  return (
    <Card
      title={
        <Space direction="vertical" size={4} style={{ width: '100%' }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
            <FileOutlined style={{ color: palette.primary }} />
            我的文件
          </span>
          <Breadcrumb
            items={breadcrumb.map((item, index) => ({
              key: index,
              title: (
                <a
                  onClick={() => handleBreadcrumbClick(index)}
                  style={{ color: index === breadcrumb.length - 1 ? palette.textSecondary : palette.primary }}
                >
                  {index === 0 && <HomeOutlined style={{ marginRight: 4 }} />}
                  {item.name}
                </a>
              ),
            }))}
          />
        </Space>
      }
      extra={
        <Space>
          <Button
            icon={<FolderAddOutlined />}
            onClick={handleCreateFolder}
            style={{ fontWeight: 500 }}
          >
            新建文件夹
          </Button>
          <Upload beforeUpload={handleUpload} showUploadList={false} multiple>
            <Button
              type="primary"
              icon={<UploadOutlined />}
              style={{
                background: palette.gradientPrimary,
                border: 'none',
                fontWeight: 600,
                boxShadow: '0 2px 10px rgba(99,102,241,0.3)',
              }}
            >
              上传文件
            </Button>
          </Upload>
        </Space>
      }
    >
      <Spin spinning={loading}>
        <Table
          columns={columns}
          dataSource={tableData}
          pagination={{ pageSize: 20 }}
          locale={{ emptyText: '暂无文件，点击右上角上传或新建文件夹' }}
        />
      </Spin>
    </Card>
  )
}

function formatSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}
