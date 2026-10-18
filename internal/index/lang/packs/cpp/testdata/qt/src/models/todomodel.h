#pragma once

#include <QAbstractListModel>
#include <QStringList>

class TodoModel : public QAbstractListModel
{
    Q_OBJECT
public:
    explicit TodoModel(QObject *parent = nullptr);
    int rowCount(const QModelIndex &parent = QModelIndex()) const override;
    QVariant data(const QModelIndex &index, int role) const override;
    void add(const QString &text);

private:
    QStringList items_;
};
