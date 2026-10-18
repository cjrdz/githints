import 'package:flutter_bloc/flutter_bloc.dart';
import '../data/db.dart';

class CartCubit extends Cubit<int> {
  CartCubit() : super(0);

  void add() => emit(state + 1);
}
