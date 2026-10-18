import 'package:shelf_router/shelf_router.dart';
import 'package:api/handlers.dart';

void main() {
  final router = Router()
    ..get('/health', health)
    ..post('/users/<id>', updateUser);
}
