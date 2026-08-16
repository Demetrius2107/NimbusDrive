import { useState } from 'react'
import { View, Text, FlatList, TouchableOpacity, StyleSheet, Alert } from 'react-native'
import * as DocumentPicker from 'expo-document-picker'
import { NativeStackScreenProps } from '@react-navigation/native-stack'
import { RootStackParamList } from '../../App'

type Props = NativeStackScreenProps<RootStackParamList, 'Files'>

interface FileItem {
  id: string
  name: string
  size: number
}

export function FilesScreen({ navigation }: Props) {
  const [files, setFiles] = useState<FileItem[]>([])

  const handlePick = async () => {
    const result = await DocumentPicker.getDocumentAsync({ multiple: true })
    if (result.canceled) return
    // TODO: 调用上传服务（分块上传走 TransferServer）
    const picked = result.assets.map((a) => ({ id: a.id, name: a.name, size: a.size ?? 0 }))
    setFiles((prev) => [...picked, ...prev])
    Alert.alert('已选择', `选中 ${picked.length} 个文件（上传待实现）`)
  }

  const renderItem = ({ item }: { item: FileItem }) => (
    <TouchableOpacity style={styles.item}>
      <Text style={styles.name} numberOfLines={1}>
        {item.name}
      </Text>
      <Text style={styles.size}>{formatSize(item.size)}</Text>
    </TouchableOpacity>
  )

  return (
    <View style={styles.container}>
      <TouchableOpacity style={styles.uploadBtn} onPress={handlePick}>
        <Text style={styles.uploadText}>+ 上传文件</Text>
      </TouchableOpacity>
      <FlatList
        data={files}
        keyExtractor={(i) => i.id}
        renderItem={renderItem}
        ListEmptyComponent={<Text style={styles.empty}>暂无文件</Text>}
      />
    </View>
  )
}

function formatSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f5f5f5' },
  uploadBtn: {
    backgroundColor: '#1677ff',
    padding: 14,
    margin: 16,
    borderRadius: 6,
    alignItems: 'center',
  },
  uploadText: { color: '#fff', fontSize: 16, fontWeight: '600' },
  item: {
    backgroundColor: '#fff',
    padding: 16,
    marginHorizontal: 16,
    marginBottom: 8,
    borderRadius: 6,
  },
  name: { fontSize: 15, fontWeight: '500' },
  size: { fontSize: 13, color: '#999', marginTop: 4 },
  empty: { textAlign: 'center', color: '#999', marginTop: 48 },
})
