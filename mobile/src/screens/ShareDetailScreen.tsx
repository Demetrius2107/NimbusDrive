import { View, Text, StyleSheet } from 'react-native'
import { NativeStackScreenProps } from '@react-navigation/native-stack'
import { RootStackParamList } from '../../App'

type Props = NativeStackScreenProps<RootStackParamList, 'ShareDetail'>

export function ShareDetailScreen({ route }: Props) {
  // TODO: GET /shares/{id} → 文件信息 + 下载
  return (
    <View style={styles.container}>
      <Text style={styles.text}>分享详情（骨架）</Text>
      <Text style={styles.id}>ID: {route.params.id}</Text>
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: '#fff' },
  text: { fontSize: 16, color: '#666' },
  id: { fontSize: 13, color: '#999', marginTop: 8 },
})
