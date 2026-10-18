#pragma once

#include <QMainWindow>
#include <QWidget>
#include "models/todomodel.h"

class TodoView;

class MainWindow : public QMainWindow
{
    Q_OBJECT
public:
    explicit MainWindow(QWidget *parent = nullptr);
    ~MainWindow() override;

private slots:
    void addTodo();

private:
    TodoModel *model_;
};

class StatusBadge final : public QObject, public QWidget {
    Q_OBJECT
};

// Not a widget: the base class is the project's own.
class Settings : public QSettingsStore {
};
