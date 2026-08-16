import { StatusBar } from 'expo-status-bar'
import { NavigationContainer } from '@react-navigation/native'
import { createNativeStackNavigator } from '@react-navigation/native-stack'
import { FilesScreen } from './src/screens/FilesScreen'
import { LoginScreen } from './src/screens/LoginScreen'
import { ShareDetailScreen } from './src/screens/ShareDetailScreen'

export type RootStackParamList = {
  Login: undefined
  Files: undefined
  ShareDetail: { id: string }
}

const Stack = createNativeStackNavigator<RootStackParamList>()

export default function App() {
  return (
    <NavigationContainer>
      <StatusBar style="auto" />
      <Stack.Navigator initialRouteName="Login">
        <Stack.Screen name="Login" component={LoginScreen} options={{ title: '登录' }} />
        <Stack.Screen name="Files" component={FilesScreen} options={{ title: 'NimbusDrive' }} />
        <Stack.Screen
          name="ShareDetail"
          component={ShareDetailScreen}
          options={{ title: '分享' }}
        />
      </Stack.Navigator>
    </NavigationContainer>
  )
}
